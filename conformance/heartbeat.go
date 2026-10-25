package conformance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/october-dev/october-bus/bus"
)

// The external-heartbeat check waits for a renewal instead of sleeping for a
// fixed window, so scheduling, network, and SQLite delays cannot fail a correct
// execution. The wait stays bounded and is still capped by the caller's context.
const (
	heartbeatRenewalTimeout = 5 * time.Second
	heartbeatPollInterval   = 25 * time.Millisecond
)

// heartbeatSession is the part of bus.AgentSession the renewal check observes.
type heartbeatSession interface {
	Err() error
	Done() <-chan struct{}
}

// awaitHeartbeatRenewal requires the given execution of agentID to renew its
// heartbeat while staying ready and reachable. It fails when the session
// reports an error or stops, when a different execution replaces it, or when
// no renewal is observed before the timeout or the context deadline.
func awaitHeartbeatRenewal(
	ctx context.Context,
	listAgents func(context.Context) ([]bus.Agent, error),
	agentID, executionID string,
	session heartbeatSession,
	timeout, pollInterval time.Duration,
) error {
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	before, err := observeExecution(waitContext, listAgents, agentID, executionID)
	if err != nil {
		return err
	}
	beforeTime, err := parseUpdatedAt(before)
	if err != nil {
		return err
	}
	last := before

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-waitContext.Done():
			return renewalTimeout(before, last, session.Err(), waitContext.Err())
		case <-session.Done():
			return fmt.Errorf("heartbeat session stopped before renewing execution %s: %v", executionID, session.Err())
		case <-ticker.C:
		}
		if err := session.Err(); err != nil {
			return fmt.Errorf("heartbeat session failed before renewing execution %s: %w", executionID, err)
		}
		current, err := observeExecution(waitContext, listAgents, agentID, executionID)
		if err != nil {
			if waitContext.Err() != nil {
				return renewalTimeout(before, last, session.Err(), waitContext.Err())
			}
			return err
		}
		last = current
		currentTime, err := parseUpdatedAt(current)
		if err != nil {
			return err
		}
		if !currentTime.After(beforeTime) {
			continue
		}
		if !current.Ready || !current.Reachable {
			return fmt.Errorf("heartbeat renewed execution %s but it is not ready and reachable: %#v", executionID, current)
		}
		return nil
	}
}

// observeExecution lists agents and requires agentID to still be held by executionID.
func observeExecution(ctx context.Context, listAgents func(context.Context) ([]bus.Agent, error), agentID, executionID string) (bus.Agent, error) {
	agents, err := listAgents(ctx)
	if err != nil {
		return bus.Agent{}, err
	}
	agent, err := findAgent(agents, agentID)
	if err != nil {
		return bus.Agent{}, err
	}
	if agent.ExecutionID != executionID {
		return bus.Agent{}, fmt.Errorf("agent %s is held by execution %q, want %q", agentID, agent.ExecutionID, executionID)
	}
	return agent, nil
}

func parseUpdatedAt(agent bus.Agent) (time.Time, error) {
	updatedAt, err := time.Parse(time.RFC3339Nano, agent.UpdatedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("agent %s has an invalid updatedAt %q: %w", agent.ID, agent.UpdatedAt, err)
	}
	return updatedAt, nil
}

func renewalTimeout(before, last bus.Agent, sessionErr, waitErr error) error {
	reason := "timed out"
	if errors.Is(waitErr, context.Canceled) {
		reason = "was canceled"
	}
	return fmt.Errorf(
		"heartbeat did not renew execution %s: wait %s (updatedAt before=%s last=%s, ready=%t reachable=%t, session error=%v)",
		before.ExecutionID, reason, before.UpdatedAt, last.UpdatedAt, last.Ready, last.Reachable, sessionErr,
	)
}
