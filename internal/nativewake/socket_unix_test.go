//go:build unix

package nativewake

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPostAuthenticatesWithoutClaimingModelDelivery(t *testing.T) {
	dir, err := os.MkdirTemp("", "ob-wake-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "inbox.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	result := make(chan []map[string]any, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		scanner := bufio.NewScanner(connection)
		var values []map[string]any
		for scanner.Scan() {
			var value map[string]any
			if json.Unmarshal(scanner.Bytes(), &value) == nil {
				values = append(values, value)
			}
		}
		result <- values
	}()
	outcome, err := Post(context.Background(), Binding{Socket: socket, Token: "session-secret"}, "Check the Bus inbox")
	if err != nil || outcome != "submitted" {
		t.Fatalf("post: %s %v", outcome, err)
	}
	values := <-result
	if len(values) != 2 || values[0]["type"] != "auth" || values[0]["token"] != "session-secret" || values[1]["type"] != "user" {
		t.Fatal("native auth/message framing failed")
	}
	if err := os.Chmod(socket, 0666); err != nil {
		t.Fatal(err)
	}
	outcome, err = Post(context.Background(), Binding{Socket: socket}, "must not send")
	if outcome != "unavailable" || err == nil {
		t.Fatal("accepted a shared socket")
	}
}
