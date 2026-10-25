package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type callOutcomeKey struct{}

// callOutcome records what the HTTP layer observed for one MCP exchange, so the
// bridge can tell a protocol-level refusal on a live session from a transport
// failure that ended the session. The SDK fails its connection on any non-2xx
// response that is not transient, even when the body is a JSON-RPC error.
type callOutcome struct {
	mu      sync.Mutex
	healthy bool
	class   connectionFailure
	reason  string
}

func withCallOutcome(ctx context.Context) (context.Context, *callOutcome) {
	outcome := &callOutcome{}
	return context.WithValue(ctx, callOutcomeKey{}, outcome), outcome
}

// observe classifies one exchange. It is invoked by the transport for every
// request carrying an outcome, including the SDK's own initialize traffic.
func (outcome *callOutcome) observe(response *http.Response, err error) {
	outcome.mu.Lock()
	defer outcome.mu.Unlock()
	switch {
	case err != nil:
		outcome.healthy = false
		outcome.class, outcome.reason = classifyTransportError(err)
	case response.StatusCode >= 200 && response.StatusCode < 300:
		outcome.healthy = true
		outcome.class, outcome.reason = "", ""
	default:
		outcome.healthy = false
		outcome.class, outcome.reason = classifyHTTPFailure(response.StatusCode, readFailureBody(response))
	}
}

func (outcome *callOutcome) failure() (connectionFailure, string) {
	outcome.mu.Lock()
	defer outcome.mu.Unlock()
	return outcome.class, outcome.reason
}

func (outcome *callOutcome) live() bool {
	outcome.mu.Lock()
	defer outcome.mu.Unlock()
	return outcome.healthy
}

// bridgeDiagnostics writes one stderr line per failure-class transition and one
// line on recovery. Messages are fixed sentences; they never include credentials,
// request bodies or authority responses. Stdout stays reserved for MCP.
type bridgeDiagnostics struct {
	mu     sync.Mutex
	out    io.Writer
	prefix string
	last   connectionFailure
}

func (diagnostics *bridgeDiagnostics) failed(class connectionFailure, reason string) {
	if diagnostics == nil {
		return
	}
	diagnostics.mu.Lock()
	defer diagnostics.mu.Unlock()
	if class == diagnostics.last {
		return
	}
	diagnostics.last = class
	fmt.Fprintf(diagnostics.out, "%s: %s: %s\n", diagnostics.prefix, class, reason)
}

func (diagnostics *bridgeDiagnostics) recovered() {
	if diagnostics == nil {
		return
	}
	diagnostics.mu.Lock()
	defer diagnostics.mu.Unlock()
	if diagnostics.last == "" {
		return
	}
	diagnostics.last = ""
	fmt.Fprintf(diagnostics.out, "%s: connection recovered\n", diagnostics.prefix)
}

// Managed workers can outlive their route/authority connection. The SDK closes
// its HTTP session on transport failures, so establish a new session for the NEXT
// tool call. Never replay the failed call: a mutation may already have committed.
type managedMCPUpstream struct {
	mu          sync.Mutex
	session     *mcp.ClientSession
	connect     func(context.Context) (*mcp.ClientSession, error)
	closed      bool
	diagnostics *bridgeDiagnostics
}

func (upstream *managedMCPUpstream) call(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	callCtx, outcome := withCallOutcome(ctx)
	upstream.mu.Lock()
	if upstream.closed {
		upstream.mu.Unlock()
		return nil, errors.New("managed MCP bridge is closed")
	}
	if err := ctx.Err(); err != nil {
		upstream.mu.Unlock()
		return nil, err
	}
	if upstream.session == nil {
		session, err := upstream.connect(callCtx)
		if err != nil {
			upstream.mu.Unlock()
			if ctx.Err() == nil {
				upstream.diagnostics.failed(outcomeFailure(outcome, err))
			}
			return nil, err
		}
		upstream.session = session
	}
	session := upstream.session
	upstream.mu.Unlock()
	result, err := session.CallTool(callCtx, params)
	if err == nil {
		upstream.diagnostics.recovered()
		return result, nil
	}
	// Ordinary cancellation keeps the session: the SDK sends the cancellation
	// notification and the authority settles the call under its own rules.
	if ctx.Err() != nil {
		return nil, err
	}
	// The authority answered on a live session, for example with invalid
	// arguments. The harness sees that error; the session stays.
	if outcome.live() && !errors.Is(err, mcp.ErrConnectionClosed) {
		return nil, err
	}
	upstream.mu.Lock()
	if upstream.session == session {
		upstream.session = nil
	}
	upstream.mu.Unlock()
	// Other calls on this failed connection may also fail. Each surfaces its
	// own outcome; none inherits permission to replay a possibly accepted tool.
	_ = session.Close()
	upstream.diagnostics.failed(outcomeFailure(outcome, err))
	return nil, err
}

// outcomeFailure prefers what the transport observed; a call that never reached
// HTTP (for example on an already-failed SDK connection) reports session loss.
func outcomeFailure(outcome *callOutcome, err error) (connectionFailure, string) {
	if class, reason := outcome.failure(); class != "" {
		return class, reason
	}
	var failure *connectionError
	if errors.As(err, &failure) {
		return failure.Class, failure.Reason
	}
	return failureSession, "the upstream MCP session ended; the next tool call reconnects"
}

func (upstream *managedMCPUpstream) close() {
	upstream.mu.Lock()
	upstream.closed = true
	session := upstream.session
	upstream.session = nil
	upstream.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
}
