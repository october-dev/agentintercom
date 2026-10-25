// Package adapters embeds host configuration alongside its adapter manifest.
// Configurations are setup data, never named-harness compatibility evidence.
package adapters

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

//go:embed */adapter.json */*.example
var files embed.FS

type Configuration struct {
	File              string `json:"file"`
	Format            string `json:"format"`
	Location          string `json:"location"`
	Executable        string `json:"executable,omitempty"`
	Docs              string `json:"docs"`
	RecommendedWaitMS int    `json:"recommendedWaitMs"`
}

type Manifest struct {
	ID            string         `json:"id"`
	HarnessFamily string         `json:"harnessFamily"`
	Status        string         `json:"status"`
	Configuration *Configuration `json:"configuration,omitempty"`
	Directory     string         `json:"-"`
}

func List() ([]Manifest, error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var result []Manifest
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := files.ReadFile(path.Join(entry.Name(), "adapter.json"))
		if err != nil {
			return nil, err
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, err
		}
		manifest.Directory = entry.Name()
		result = append(result, manifest)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Directory < result[j].Directory })
	return result, nil
}

func Lookup(host string) (Manifest, error) {
	all, err := List()
	if err != nil {
		return Manifest{}, err
	}
	for _, manifest := range all {
		if host == manifest.Directory || host == manifest.ID {
			if manifest.Configuration == nil {
				return Manifest{}, fmt.Errorf("%s needs native setup; see adapters/%s/README.md", host, manifest.Directory)
			}
			return manifest, nil
		}
	}
	return Manifest{}, fmt.Errorf("no reviewed configuration for %q; use agentintercom harness list", host)
}

// Render substitutes entire string values, not shell fragments. JSON quoting
// also works for the simple TOML and YAML string values in these templates.
func Render(manifest Manifest, values map[string]string) ([]byte, error) {
	if manifest.Configuration == nil {
		return nil, fmt.Errorf("adapter has no configuration")
	}
	file := manifest.Configuration.File
	if path.Base(file) != file {
		return nil, fmt.Errorf("invalid embedded configuration path")
	}
	data, err := files.ReadFile(path.Join(manifest.Directory, file))
	if err != nil {
		return nil, err
	}
	replacements := make([]string, 0, len(values)*2)
	for _, key := range []string{"command", "scope", "agent", "name", "dataDir", "runtimeDir"} {
		value := values[key]
		// Hosts expand dollar expressions differently; Crush can execute them.
		// Reject rather than emitting a config whose meaning changes at load time.
		if value == "" || strings.ContainsAny(value, "$`\x00\r\n") {
			return nil, fmt.Errorf("%s must be nonempty and contain no shell expansion or control characters", key)
		}
		encoded, _ := json.Marshal(value)
		replacements = append(replacements, `"{{`+key+`}}"`, string(encoded))
	}
	result := []byte(strings.NewReplacer(replacements...).Replace(string(data)))
	if strings.Contains(string(result), "{{") {
		return nil, fmt.Errorf("unresolved configuration placeholder")
	}
	if manifest.Configuration.Format == "json" && !json.Valid(result) {
		return nil, fmt.Errorf("invalid rendered JSON")
	}
	return result, nil
}
