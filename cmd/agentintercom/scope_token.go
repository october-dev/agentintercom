package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// maxScopeTokenBytes bounds a scope credential read from a file or an
// inherited descriptor. A token is 43 characters; the rest allows a newline.
const maxScopeTokenBytes = 128

var errScopeTokenFile = errors.New("scope token must be an owner-only regular file of at most 128 bytes")

// parseScopeToken accepts one scope credential in the shape every authority
// mints (the shape validExecutionToken checks: 32 random bytes, canonical
// unpadded base64url), with surrounding whitespace removed. The canonical
// check also refuses line breaks inside the value, which the base64 decoder
// alone would skip. Errors name the source, never the content.
func parseScopeToken(data []byte, source string) (string, error) {
	token := strings.TrimSpace(string(data))
	if !validExecutionToken(token) {
		return "", fmt.Errorf("%s is invalid", source)
	}
	return token, nil
}

func privateScopeTokenFile(info os.FileInfo) bool {
	// Windows uses the user's profile ACL; Unix mode bits are not a Windows ACL.
	return info.Mode().IsRegular() && info.Size() <= maxScopeTokenBytes &&
		(runtime.GOOS == "windows" || info.Mode().Perm()&0o077 == 0)
}

// readScopeTokenFile reads a scope credential once from a private file. The
// path must name a regular file, not a symlink, with owner-only permissions on
// Unix, and the file opened must be the file inspected.
func readScopeTokenFile(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !privateScopeTokenFile(before) {
		return "", errScopeTokenFile
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, after) || !privateScopeTokenFile(after) {
		return "", errScopeTokenFile
	}
	data, err := io.ReadAll(io.LimitReader(file, maxScopeTokenBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxScopeTokenBytes {
		return "", errScopeTokenFile
	}
	return parseScopeToken(data, "scope token file")
}

// readScopeTokenFD reads a scope credential from a descriptor the caller left
// open across exec, typically the read end of a pipe, so the credential never
// appears in this process's environment, arguments or on disk. It reads up to
// the first newline or end of file, whichever comes first, so a writer need not
// close its end. The descriptor is closed before returning, success or not, so
// the managed command never inherits it.
func readScopeTokenFD(fd int) (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("--scope-token-fd is not supported on Windows; use --scope-token-file")
	}
	// 0-2 are the managed command's standard streams.
	if fd < 3 {
		return "", errors.New("--scope-token-fd must name an inherited descriptor of 3 or more")
	}
	file := os.NewFile(uintptr(fd), "scope-token-fd")
	if file == nil {
		return "", errors.New("--scope-token-fd names an invalid descriptor")
	}
	defer file.Close()
	line, err := bufio.NewReader(io.LimitReader(file, maxScopeTokenBytes+1)).ReadSlice('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("could not read --scope-token-fd: %w", err)
	}
	if len(line) > maxScopeTokenBytes {
		return "", errors.New("--scope-token-fd held more than 128 bytes before a newline")
	}
	return parseScopeToken(line, "scope token from --scope-token-fd")
}
