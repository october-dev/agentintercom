package bus

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ScopeTokenPath uses a case-sensitive digest, not the scope ID as a filename:
// protocol IDs may contain colons or differ only by case, including on Windows.
func ScopeTokenPath(dataDir, scopeID string) (string, error) {
	if err := validateIdentity(scopeID, "scopeId", false); err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "scopes", fmt.Sprintf("%x.token", sha256.Sum256([]byte(scopeID)))), nil
}

func validLocalScopeToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32
}

// LockScopeCredentials serializes local CLI authority mutations through both
// the server response and the cache write. Atomic rename alone cannot prevent
// two rotations from saving their responses in the wrong order.
func LockScopeCredentials(dataDir string) (*os.File, error) {
	if err := secureDirectory(dataDir); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "scope-credentials.lock")
	if _, err := os.Lstat(path); err == nil {
		if err := checkPrivatePath(path, false); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	file, err := acquireFileLock(path)
	if err != nil {
		return nil, fmt.Errorf("cannot lock local scope credentials; another CLI mutation may be in progress: %w", err)
	}
	return file, nil
}

func checkPrivatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("%s must be a real %s", path, map[bool]string{true: "directory", false: "file"}[directory])
	}
	// Windows uses the user's profile ACL; Unix mode bits are not a Windows ACL.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must have owner-only permissions", path)
	}
	if !directory && info.Size() > 128 {
		return fmt.Errorf("scope credential file is invalid")
	}
	return nil
}

// SaveScopeToken atomically stores a local scope credential. It never prints it.
// Like the daemon run file, this is not a security boundary against the same OS user.
func SaveScopeToken(dataDir, scopeID, token string) error {
	path, err := ScopeTokenPath(dataDir, scopeID)
	if err != nil {
		return err
	}
	if !validLocalScopeToken(token) {
		return fmt.Errorf("scope credential is invalid")
	}
	if err := secureDirectory(dataDir); err != nil {
		return err
	}
	if err := secureDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if err := checkPrivatePath(path, false); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".scope-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.WriteString(token + "\n"); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// missingScopeCredential reports an absent local credential. It matches
// fs.ErrNotExist without appending the hashed file path to the message.
type missingScopeCredential string

func (e missingScopeCredential) Error() string { return string(e) }
func (e missingScopeCredential) Unwrap() error { return fs.ErrNotExist }

// ReadScopeToken fails closed on missing, malformed, shared, or symlinked files.
func ReadScopeToken(dataDir, scopeID string) (string, error) {
	path, err := ScopeTokenPath(dataDir, scopeID)
	if err != nil {
		return "", err
	}
	// A missing file almost always means the workspace was never created on this
	// machine (or under these data directories); say so instead of printing a
	// hashed credential path.
	missing := func() error {
		return missingScopeCredential(fmt.Sprintf("workspace %q has no local credential in %s; create it with agentintercom workspace create %s, or recover an existing one with agentintercom scope rotate-token --id %s", scopeID, dataDir, scopeID, scopeID))
	}
	for _, directory := range []string{dataDir, filepath.Dir(path)} {
		if err := checkPrivatePath(directory, true); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", missing()
			}
			return "", fmt.Errorf("cannot read local scope credential: %w", err)
		}
	}
	if err := checkPrivatePath(path, false); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", missing()
		}
		return "", fmt.Errorf("cannot read local scope credential; create the scope locally or rotate its token: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if !validLocalScopeToken(token) {
		return "", fmt.Errorf("local scope credential is invalid; rotate the scope token")
	}
	return token, nil
}

// RemoveScopeToken removes only the named scope's local credential after deletion.
func RemoveScopeToken(dataDir, scopeID string) error {
	path, err := ScopeTokenPath(dataDir, scopeID)
	if err != nil {
		return err
	}
	for _, directory := range []string{dataDir, filepath.Dir(path)} {
		if err := checkPrivatePath(directory, true); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
