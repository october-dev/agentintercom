package bus

import (
	"fmt"
	"os"
	"path/filepath"
)

// Lock files are persistent: unlinking one can split waiters across inodes.
func acquireFileLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("Agent Intercom store or runtime is already owned: %w", err)
	}
	return file, nil
}

func canonicalDatabasePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(absolute); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("database must be a regular file")
		}
		if err := validateDatabaseLinks(absolute, info); err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(absolute)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}
