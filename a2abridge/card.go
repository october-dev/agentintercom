package a2abridge

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

const bearerSchemeName a2a.SecuritySchemeName = "bearer"

// CardOptions configures the content of a generated A2A Agent Card. The HTTP
// delivery behaviour of the resulting card is controlled separately by
// HandlerOptions in a2abridge/handler.go.
type CardOptions struct {
	InterfaceURL string

	// ProviderOrganization names the organization that publishes the agent.
	// ProviderOrganization and ProviderURL must be set together.
	ProviderOrganization string

	// ProviderURL points at the provider's public website or relevant
	// documentation. It is validated like DocumentationURL and IconURL.
	ProviderURL string

	// DocumentationURL is an optional link to the agent's documentation.
	DocumentationURL string

	// IconURL is an optional URL to an icon representing the agent.
	IconURL string

	Version     string
	Description string
}

type Capability struct {
	Name        string
	Description string
}

type AgentProfile struct {
	DisplayName  string
	Capabilities []Capability
}

func NewAgentCard(agent AgentProfile, options CardOptions) (*a2a.AgentCard, error) {
	if strings.TrimSpace(agent.DisplayName) == "" {
		return nil, errors.New("agent display name is required")
	}
	if err := validateInterfaceURL(options.InterfaceURL); err != nil {
		return nil, err
	}
	providerOrganization := strings.TrimSpace(options.ProviderOrganization)
	if options.ProviderOrganization != "" && providerOrganization == "" {
		return nil, errors.New("providerOrganization must not be blank")
	}
	if (providerOrganization == "") != (options.ProviderURL == "") {
		return nil, errors.New("providerOrganization and providerUrl must be set together")
	}
	for _, field := range []struct {
		name, value string
	}{
		{"providerUrl", options.ProviderURL},
		{"documentationUrl", options.DocumentationURL},
		{"iconUrl", options.IconURL},
	} {
		if field.value != "" {
			if err := validatePublicURL(field.value); err != nil {
				return nil, errors.New(field.name + ": " + err.Error())
			}
		}
	}
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.Description == "" {
		options.Description = "AI agent connected through Agent Intercom."
	}

	skills := make([]a2a.AgentSkill, 0, len(agent.Capabilities))
	for _, capability := range agent.Capabilities {
		description := capability.Description
		if description == "" {
			description = "Supports " + capability.Name + "."
		}
		skills = append(skills, a2a.AgentSkill{
			ID:          capability.Name,
			Name:        capability.Name,
			Description: description,
			Tags:        []string{capability.Name},
		})
	}

	card := &a2a.AgentCard{
		Name:        agent.DisplayName,
		Description: options.Description,
		Version:     options.Version,
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(options.InterfaceURL, a2a.TransportProtocolHTTPJSON),
		},
		Capabilities:       a2a.AgentCapabilities{},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             skills,
		SecuritySchemes: a2a.NamedSecuritySchemes{
			bearerSchemeName: a2a.HTTPAuthSecurityScheme{
				Scheme:      "Bearer",
				Description: "Agent Intercom A2A credential.",
			},
		},
		SecurityRequirements: a2a.SecurityRequirementsOptions{
			{bearerSchemeName: a2a.SecuritySchemeScopes{}},
		},
	}
	if providerOrganization != "" {
		card.Provider = &a2a.AgentProvider{
			Org: providerOrganization,
			URL: options.ProviderURL,
		}
	}
	card.DocumentationURL = options.DocumentationURL
	card.IconURL = options.IconURL
	return card, nil
}

func validateInterfaceURL(value string) error {
	if len(value) > 4096 {
		return errors.New("A2A interface URL exceeds 4096 bytes")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("A2A interface URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("A2A interface URL must not include credentials, a query, or a fragment")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return errors.New("non-loopback A2A interfaces require HTTPS")
	}
	return nil
}

// validatePublicURL checks that a URL embedded in a public Agent Card is
// safe to publish. It is stricter than a generic URL parser because cards
// are read by other agents, cached by browsers, and indexed by search
// engines. The interface URL uses a tighter contract (validateInterfaceURL)
// because clients actually call it.
func validatePublicURL(value string) error {
	if len(value) > 4096 {
		return errors.New("URL exceeds 4096 bytes")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must not include credentials, a query, or a fragment")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
