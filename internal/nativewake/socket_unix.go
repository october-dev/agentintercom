//go:build unix

package nativewake

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Binding is supplied by the session's own SessionStart hook. Its token never
// leaves this machine or appears in a provider command line.
type Binding struct {
	Socket string `json:"socket"`
	Token  string `json:"token,omitempty"`
}

func ValidateBinding(binding Binding) error {
	if !filepath.IsAbs(binding.Socket) || len(binding.Socket) > 103 || len(binding.Token) > 1024 {
		return errors.New("native inbox binding is invalid")
	}
	for _, entry := range []struct {
		path   string
		socket bool
	}{{filepath.Dir(binding.Socket), false}, {binding.Socket, true}} {
		info, err := os.Lstat(entry.path)
		if err != nil {
			return errors.New("native inbox is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || (entry.socket && info.Mode()&os.ModeSocket == 0) || (!entry.socket && !info.IsDir()) {
			return errors.New("native inbox must be an owner-only socket in a private directory")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			return errors.New("native inbox belongs to another operating-system user")
		}
	}
	return nil
}

// Post sends only a nudge to fetch the durable Bus inbox, never another person's
// message body. A successful write is a submission, not model acknowledgment.
// Once any bytes may have crossed the socket boundary, failure is ambiguous.
func Post(ctx context.Context, binding Binding, text string) (outcome string, err error) {
	if err := ValidateBinding(binding); err != nil {
		return "unavailable", err
	}
	connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", binding.Socket)
	if err != nil {
		return "unavailable", errors.New("native inbox connection failed")
	}
	defer connection.Close()
	deadline := time.Now().Add(3 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	_ = connection.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	var payload []byte
	if binding.Token != "" {
		auth, _ := json.Marshal(map[string]string{"type": "auth", "token": binding.Token})
		payload = append(auth, '\n')
	}
	message, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": text}})
	payload = append(payload, message...)
	payload = append(payload, '\n')
	written, err := connection.Write(payload)
	if err != nil || written != len(payload) {
		return "uncertain", errors.New("native inbox submission could not be confirmed")
	}
	if socket, ok := connection.(*net.UnixConn); ok {
		if err := socket.CloseWrite(); err != nil {
			return "uncertain", errors.New("native inbox submission could not be confirmed")
		}
	}
	return "submitted", nil
}
