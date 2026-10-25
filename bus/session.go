package bus

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"
)

// AgentSessionOptions describes one managed agent execution.
type AgentSessionOptions struct {
	Address           string
	ScopeToken        string
	DeviceToken       string // Hosted device authority; mutually exclusive with ScopeToken.
	Registration      RegisterAgentInput
	HeartbeatInterval time.Duration
	InitialLifecycle  AgentLifecycle
	InitialReady      bool
	HTTP              *http.Client
}

// AgentSession keeps an agent execution registered and reachable while its
// harness process is running.
type AgentSession struct {
	Address      string
	Registration RegisterAgentResult
	Client       Client

	retryTransient bool
	leaseMS        int64
	context        context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	beatMu         sync.Mutex
	stateMu        sync.Mutex
	lifecycle      AgentLifecycle
	ready          bool
	errMu          sync.Mutex
	err            error
	close          sync.Once
	closed         bool // guarded by stateMu
}

// StartAgentSession registers an execution and starts its heartbeat loop.
func StartAgentSession(ctx context.Context, options AgentSessionOptions) (*AgentSession, error) {
	if options.ScopeToken != "" && options.DeviceToken != "" {
		return nil, Errorf(CodeInvalidArgument, "use workspace or device authority, not both")
	}
	leaseMS, err := normalizedLease(options.Registration.LeaseMS)
	if err != nil {
		return nil, err
	}
	interval := options.HeartbeatInterval
	if interval == 0 {
		interval = time.Duration(leaseMS) * time.Millisecond / 3
	}
	if interval <= 0 || interval >= time.Duration(leaseMS)*time.Millisecond {
		return nil, Errorf(CodeInvalidArgument, "heartbeat interval must be shorter than the execution lease")
	}
	lifecycle := options.InitialLifecycle
	if lifecycle == "" {
		lifecycle = LifecycleStarting
	}
	if err := validateLifecycle(lifecycle); err != nil {
		return nil, err
	}
	if lifecycle == LifecycleOffline && options.InitialReady {
		return nil, Errorf(CodeInvalidArgument, "offline agents cannot be ready")
	}
	registrationInput := options.Registration
	registrationInput.LeaseMS = leaseMS
	scopeClient := Client{Address: options.Address, Token: options.ScopeToken, HTTP: options.HTTP}
	// Check before registration: an incompatible daemon must not replace an
	// existing execution and then fail only when the new session is closed.
	health, err := scopeClient.Health(ctx)
	if err != nil {
		return nil, err
	}
	if health.Name != "october-bus" || health.ProtocolVersion != ProtocolVersion ||
		health.Status != "ready" || !slices.Contains(health.Features, FeatureSessionRetirement) {
		return nil, Errorf(CodeConflict, "managed sessions require a ready protocol 0.1 runtime advertising session-retirement; upgrade the daemon before registering")
	}
	var registration RegisterAgentResult
	if options.DeviceToken != "" {
		deviceClient := Client{Address: options.Address, Token: options.DeviceToken, HTTP: options.HTTP}
		registration, err = deviceClient.StartDeviceSession(ctx, registrationInput.DisplayName, registrationInput.ID)
	} else {
		registration, err = scopeClient.RegisterAgent(ctx, registrationInput)
	}
	if err != nil {
		return nil, err
	}
	agentClient := Client{Address: options.Address, Token: registration.AgentToken, HTTP: options.HTTP}
	if _, err := agentClient.Heartbeat(ctx, HeartbeatInput{Lifecycle: lifecycle, Ready: options.InitialReady, LeaseMS: leaseMS}); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = agentClient.Retire(cleanupCtx)
		return nil, err
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	session := &AgentSession{
		Address: options.Address, Registration: registration, Client: agentClient,
		retryTransient: options.DeviceToken != "", leaseMS: leaseMS, context: heartbeatCtx, cancel: cancel, done: make(chan struct{}),
		lifecycle: lifecycle, ready: options.InitialReady,
	}
	go session.heartbeat(heartbeatCtx, interval)
	return session, nil
}

func (s *AgentSession) heartbeat(ctx context.Context, interval time.Duration) {
	defer close(s.done)
	defer func() {
		s.cancel()
		s.stateMu.Lock()
		s.closed = true
		s.stateMu.Unlock()
		s.beatMu.Lock()
		defer s.beatMu.Unlock()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Client.Retire(cleanupCtx); err != nil {
			s.errMu.Lock()
			if s.err == nil {
				s.err = err
			}
			s.errMu.Unlock()
		}
	}()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	leaseDeadline := time.Now().Add(time.Duration(s.leaseMS) * time.Millisecond)
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if _, err := s.sendHeartbeat(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				code := AsBusError(err).Code
				if s.retryTransient && (code == CodeInternal || code == CodeBackpressure) && time.Now().Before(leaseDeadline.Add(-time.Second)) {
					timer.Reset(min(interval, 5*time.Second))
					continue
				}
				s.errMu.Lock()
				s.err = err
				s.errMu.Unlock()
				return
			}
			leaseDeadline = time.Now().Add(time.Duration(s.leaseMS) * time.Millisecond)
			timer.Reset(interval)
		}
	}
}

func (s *AgentSession) sendHeartbeat(ctx context.Context) (Agent, error) {
	s.beatMu.Lock()
	defer s.beatMu.Unlock()
	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return Agent{}, Errorf(CodeConflict, "Agent session is closed")
	}
	lifecycle, ready := s.lifecycle, s.ready
	s.stateMu.Unlock()
	return s.Client.Heartbeat(ctx, HeartbeatInput{Lifecycle: lifecycle, Ready: ready, LeaseMS: s.leaseMS})
}

// SetState updates the lifecycle and readiness reported by this execution.
func (s *AgentSession) SetState(ctx context.Context, lifecycle AgentLifecycle, ready bool) (Agent, error) {
	s.stateMu.Lock()
	closed := s.closed
	s.stateMu.Unlock()
	if closed {
		return Agent{}, Errorf(CodeConflict, "Agent session is closed")
	}
	if err := validateLifecycle(lifecycle); err != nil {
		return Agent{}, err
	}
	if lifecycle == LifecycleOffline && ready {
		return Agent{}, Errorf(CodeInvalidArgument, "offline agents cannot be ready")
	}
	s.beatMu.Lock()
	defer s.beatMu.Unlock()
	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return Agent{}, Errorf(CodeConflict, "Agent session is closed")
	}
	s.stateMu.Unlock()
	operationContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.context, cancel)
	defer stop()
	defer cancel()
	agent, err := s.Client.Heartbeat(operationContext, HeartbeatInput{Lifecycle: lifecycle, Ready: ready, LeaseMS: s.leaseMS})
	if err == nil {
		s.stateMu.Lock()
		s.lifecycle, s.ready = lifecycle, ready
		s.stateMu.Unlock()
	}
	return agent, err
}

// Done closes after heartbeat termination and the bounded retirement attempt.
func (s *AgentSession) Done() <-chan struct{} { return s.done }

// Err returns the first heartbeat or retirement failure, if any.
func (s *AgentSession) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

// Close stops heartbeats, retires authority and releases execution obligations.
func (s *AgentSession) Close(ctx context.Context) error {
	s.close.Do(func() {
		s.stateMu.Lock()
		s.closed = true
		s.stateMu.Unlock()
		s.cancel()
	})
	select {
	case <-s.done:
		return s.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
