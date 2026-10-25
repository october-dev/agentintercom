package conformance

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/october-dev/october-bus/bus"
)

const testExecution = "exec-1"

var heartbeatEpoch = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

type fakeHeartbeatSession struct {
	mu   sync.Mutex
	err  error
	done chan struct{}
}

func newFakeHeartbeatSession() *fakeHeartbeatSession {
	return &fakeHeartbeatSession{done: make(chan struct{})}
}

func (s *fakeHeartbeatSession) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *fakeHeartbeatSession) Done() <-chan struct{} { return s.done }

func (s *fakeHeartbeatSession) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// scriptedAgents returns one agent snapshot per ListAgents call and repeats the
// last snapshot once the script is exhausted.
func scriptedAgents(snapshots ...bus.Agent) func(context.Context) ([]bus.Agent, error) {
	var mu sync.Mutex
	calls := 0
	return func(ctx context.Context) ([]bus.Agent, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		index := min(calls, len(snapshots)-1)
		calls++
		return []bus.Agent{snapshots[index]}, nil
	}
}

func workerAt(offset time.Duration) bus.Agent {
	return bus.Agent{
		ID: "worker", ExecutionID: testExecution, Ready: true, Reachable: true,
		UpdatedAt: heartbeatEpoch.Add(offset).Format(time.RFC3339Nano),
	}
}

func TestAwaitHeartbeatRenewalToleratesDelayedRenewal(t *testing.T) {
	stale := workerAt(0)
	// Several polls see no renewal, as when the heartbeat goroutine or SQLite
	// write is delayed well past the heartbeat interval.
	list := scriptedAgents(stale, stale, stale, stale, stale, stale, workerAt(time.Second))
	err := awaitHeartbeatRenewal(context.Background(), list, "worker", testExecution,
		newFakeHeartbeatSession(), time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("delayed renewal should pass: %v", err)
	}
}

func TestAwaitHeartbeatRenewalFailsWithoutHeartbeats(t *testing.T) {
	err := awaitHeartbeatRenewal(context.Background(), scriptedAgents(workerAt(0)), "worker", testExecution,
		newFakeHeartbeatSession(), 50*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("disabled heartbeats must fail the check")
	}
	for _, want := range []string{"did not renew", "timed out", heartbeatEpoch.Format(time.RFC3339Nano)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("diagnostic %q is missing %q", err, want)
		}
	}
}

func TestAwaitHeartbeatRenewalRejectsReplacementExecution(t *testing.T) {
	replacement := workerAt(time.Second)
	replacement.ExecutionID = "exec-2"
	err := awaitHeartbeatRenewal(context.Background(), scriptedAgents(workerAt(0), replacement), "worker", testExecution,
		newFakeHeartbeatSession(), time.Second, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "exec-2") {
		t.Fatalf("a replacement execution must not count as renewal: %v", err)
	}
}

func TestAwaitHeartbeatRenewalRejectsUnreadyRenewal(t *testing.T) {
	unready := workerAt(time.Second)
	unready.Ready = false
	err := awaitHeartbeatRenewal(context.Background(), scriptedAgents(workerAt(0), unready), "worker", testExecution,
		newFakeHeartbeatSession(), time.Second, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not ready and reachable") {
		t.Fatalf("an unready renewal must fail: %v", err)
	}
}

func TestAwaitHeartbeatRenewalReportsSessionError(t *testing.T) {
	session := newFakeHeartbeatSession()
	session.fail(errors.New("heartbeat rejected"))
	err := awaitHeartbeatRenewal(context.Background(), scriptedAgents(workerAt(0), workerAt(time.Second)), "worker", testExecution,
		session, time.Second, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "heartbeat rejected") {
		t.Fatalf("a session error must fail the check: %v", err)
	}
}

func TestAwaitHeartbeatRenewalReportsStoppedSession(t *testing.T) {
	session := newFakeHeartbeatSession()
	close(session.done)
	err := awaitHeartbeatRenewal(context.Background(), scriptedAgents(workerAt(0)), "worker", testExecution,
		session, time.Second, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("a stopped session must fail the check: %v", err)
	}
}

func TestAwaitHeartbeatRenewalRespectsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := awaitHeartbeatRenewal(ctx, scriptedAgents(workerAt(0)), "worker", testExecution,
		newFakeHeartbeatSession(), time.Minute, time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("wait ignored the context deadline: %s", elapsed)
	}
}
