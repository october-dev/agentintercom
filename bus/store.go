package bus

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	schemaVersion                = 9
	reservationTTL               = 30 * time.Second
	messageBacklogCap            = 10000
	activeTaskCap                = 5000
	pendingEscalationCapPerAgent = 100
	pendingEscalationCapPerScope = 1000
)

type Principal struct {
	AgentIdentity
	LeaseExpiresAt int64 `json:"-"`
}

// CredentialKind identifies a currently valid non-scope bearer credential.
type CredentialKind int

const (
	CredentialKindUnknown CredentialKind = iota
	CredentialKindAgent
	CredentialKindScopedPrincipal
)

type Store struct {
	db     *sql.DB
	lock   *os.File
	source string
}

type rowScanner interface {
	Scan(...any) error
}

func OpenStore(source string) (*Store, error) {
	var lock *os.File
	if source != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
			return nil, err
		}
		var err error
		source, err = canonicalDatabasePath(source)
		if err != nil {
			return nil, err
		}
		lock, err = acquireFileLock(source + ".lock")
		if err != nil {
			return nil, err
		}
		file, err := os.OpenFile(source, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			lock.Close()
			return nil, err
		}
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			lock.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			lock.Close()
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", source)
	if err != nil {
		if lock != nil {
			lock.Close()
		}
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, lock: lock, source: source}
	if err := store.initialize(context.Background()); err != nil {
		db.Close()
		if lock != nil {
			lock.Close()
		}
		return nil, err
	}
	if _, err := db.Exec(operationalIndexesSQL); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	err := s.db.Close()
	if s.lock != nil {
		_ = s.lock.Close()
	}
	return err
}

func (s *Store) Backend() StorageBackend { return StorageBackendSQLite }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) initialize(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=2500;`); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == 2 {
		return s.migrateV2(ctx)
	}
	if version != 0 && version != schemaVersion {
		return fmt.Errorf("database schema %d does not match %d", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "BEGIN IMMEDIATE;"+schemaSQL+"COMMIT;")
	return err
}

func nowMillis() int64 { return time.Now().UnixMilli() }

func instant(value int64) string {
	if value == 0 {
		return ""
	}
	return time.UnixMilli(value).UTC().Format(time.RFC3339Nano)
}

func randomValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func randomID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value), nil
}

func tokenDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func secureEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func jsonValue(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}

func unique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func isSQLiteConstraint(err error) bool {
	var sqliteError *sqlite.Error
	return errors.As(err, &sqliteError) && sqliteError.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}

func (s *Store) CreateScope(ctx context.Context, requestedID string) (CreateScopeResult, error) {
	scopeID := requestedID
	if scopeID == "" {
		var err error
		scopeID, err = randomID("scope_")
		if err != nil {
			return CreateScopeResult{}, err
		}
	}
	token, err := randomValue(32)
	if err != nil {
		return CreateScopeResult{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO scopes(scope_id,token_hash,created_at) VALUES(?,?,?)`, scopeID, tokenDigest(token), nowMillis())
	if err != nil {
		if isSQLiteConstraint(err) {
			return CreateScopeResult{}, Errorf(CodeConflict, "Scope "+scopeID+" already exists")
		}
		return CreateScopeResult{}, err
	}
	return CreateScopeResult{ScopeID: scopeID, ScopeToken: token}, nil
}

func (s *Store) AuthenticateScope(ctx context.Context, supplied string) (string, error) {
	hash := tokenDigest(supplied)
	var scopeID string
	err := s.db.QueryRowContext(ctx, `SELECT scope_id FROM scopes WHERE token_hash=?`, hash).Scan(&scopeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Errorf(CodeUnauthenticated, "Invalid scope token")
	}
	if err != nil {
		return "", err
	}
	return scopeID, nil
}

// CredentialKind classifies a non-scope bearer credential after scope
// authentication has failed. It intentionally does not resolve scope tokens.
func (s *Store) CredentialKind(ctx context.Context, supplied string) (CredentialKind, error) {
	var leaseExpiresAt int64
	err := s.db.QueryRowContext(ctx, `SELECT lease_expires_at FROM agents WHERE token_hash=?`, tokenDigest(supplied)).Scan(&leaseExpiresAt)
	if err == nil {
		if leaseExpiresAt > nowMillis() {
			return CredentialKindAgent, nil
		}
		return CredentialKindUnknown, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CredentialKindUnknown, err
	}

	credentialID, secret, valid := splitScopedCredential(supplied)
	if !valid {
		secureEqual(tokenDigest("invalid"), tokenDigest(supplied))
		return CredentialKindUnknown, nil
	}
	var expectedHash string
	var enabled int
	err = s.db.QueryRowContext(ctx, `SELECT token_hash,enabled FROM scoped_credentials WHERE credential_id=?`, credentialID).
		Scan(&expectedHash, &enabled)
	actualHash := tokenDigest(secret)
	if errors.Is(err, sql.ErrNoRows) {
		secureEqual(tokenDigest("invalid"), actualHash)
		return CredentialKindUnknown, nil
	}
	if err != nil {
		return CredentialKindUnknown, err
	}
	match := secureEqual(expectedHash, actualHash)
	if enabled == 1 && match {
		return CredentialKindScopedPrincipal, nil
	}
	return CredentialKindUnknown, nil
}

func (s *Store) RegisterAgent(ctx context.Context, scopeID string, input RegisterAgentInput) (RegisterAgentResult, error) {
	agentID := input.ID
	var err error
	if agentID == "" {
		agentID, err = randomID("agent_")
		if err != nil {
			return RegisterAgentResult{}, err
		}
	}
	executionID, err := randomID("exec_")
	if err != nil {
		return RegisterAgentResult{}, err
	}
	agentToken, err := randomValue(32)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	capabilities, err := jsonValue(input.Capabilities)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	now := nowMillis()
	leaseExpiresAt := now + input.LeaseMS
	tx, err := s.beginTx(ctx)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
INSERT INTO agents(scope_id,agent_id,display_name,capabilities_json,execution_id,token_hash,lifecycle,ready,lease_expires_at,registered_at,updated_at)
VALUES(?,?,?,?,?,?,'starting',0,?,?,?)
ON CONFLICT(scope_id,agent_id) DO UPDATE SET
 display_name=excluded.display_name, capabilities_json=excluded.capabilities_json,
 execution_id=excluded.execution_id, token_hash=excluded.token_hash, lifecycle='starting', ready=0,
 lease_expires_at=excluded.lease_expires_at, registered_at=excluded.registered_at, updated_at=excluded.updated_at`,
		scopeID, agentID, input.DisplayName, capabilities, executionID, tokenDigest(agentToken), leaseExpiresAt, now, now)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	if err := releaseAgentReservations(ctx, tx, scopeID, agentID); err != nil {
		return RegisterAgentResult{}, err
	}
	if err := releaseStaleTaskClaims(ctx, tx, scopeID); err != nil {
		return RegisterAgentResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE a2a_publications SET updated_at=? WHERE scope_id=? AND agent_id=?`, now, scopeID, agentID); err != nil {
		return RegisterAgentResult{}, err
	}
	if err := appendEvent(ctx, tx, scopeID, "agent.registered", agentID, eventAttributes(
		"executionId", executionID, "lifecycle", string(LifecycleStarting), "ready", "false", "leaseExpiresAt", instant(leaseExpiresAt),
	), now); err != nil {
		return RegisterAgentResult{}, err
	}
	for _, peer := range unique(input.ConnectTo) {
		created, err := linkAgents(ctx, tx, scopeID, agentID, peer, now)
		if err != nil {
			return RegisterAgentResult{}, err
		}
		if created {
			a, b := orderedPeers(agentID, peer)
			if err := appendEvent(ctx, tx, scopeID, "link.created", a+":"+b, eventAttributes("leftAgent", a, "rightAgent", b), now); err != nil {
				return RegisterAgentResult{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return RegisterAgentResult{}, err
	}
	return RegisterAgentResult{
		AgentIdentity: AgentIdentity{ScopeID: scopeID, AgentID: agentID, ExecutionID: executionID},
		AgentToken:    agentToken, LeaseExpiresAt: instant(leaseExpiresAt),
	}, nil
}

// CurrentAgentToken resolves the execution that currently owns a token,
// regardless of its lease. A replaced token is unknown. Callers decide whether
// an expired or retired lease is acceptable; only retirement accepts one.
func (s *Store) CurrentAgentToken(ctx context.Context, supplied string) (Principal, error) {
	hash := tokenDigest(supplied)
	var principal Principal
	err := s.db.QueryRowContext(ctx, `SELECT scope_id,agent_id,execution_id,lease_expires_at FROM agents WHERE token_hash=?`, hash).
		Scan(&principal.ScopeID, &principal.AgentID, &principal.ExecutionID, &principal.LeaseExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, Errorf(CodeUnauthenticated, "Invalid agent token")
	}
	if err != nil {
		return Principal{}, err
	}
	return principal, nil
}

func (s *Store) AuthenticateAgent(ctx context.Context, supplied string) (Principal, error) {
	principal, err := s.CurrentAgentToken(ctx, supplied)
	if err != nil {
		return Principal{}, err
	}
	if principal.LeaseExpiresAt <= nowMillis() {
		return Principal{}, Errorf(CodeUnauthenticated, "Agent execution lease has expired")
	}
	return principal, nil
}

const agentColumns = `agent_id,display_name,capabilities_json,lifecycle,ready,execution_id,lease_expires_at,registered_at,updated_at`

func scanAgent(row rowScanner) (Agent, error) {
	var agent Agent
	var capabilities string
	var ready int
	var leaseExpiresAt, registeredAt, updatedAt int64
	if err := row.Scan(&agent.ID, &agent.DisplayName, &capabilities, &agent.Lifecycle, &ready, &agent.ExecutionID, &leaseExpiresAt, &registeredAt, &updatedAt); err != nil {
		return Agent{}, err
	}
	if err := json.Unmarshal([]byte(capabilities), &agent.Capabilities); err != nil {
		return Agent{}, err
	}
	if agent.Capabilities == nil {
		agent.Capabilities = []AgentCapability{}
	}
	agent.Reachable = leaseExpiresAt > nowMillis() && agent.Lifecycle != LifecycleOffline
	agent.Ready = agent.Reachable && ready == 1
	if !agent.Reachable {
		agent.Lifecycle = LifecycleOffline
	}
	agent.RegisteredAt = instant(registeredAt)
	agent.UpdatedAt = instant(updatedAt)
	return agent, nil
}

func (s *Store) Agent(ctx context.Context, scopeID, agentID string) (Agent, error) {
	agent, err := scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentColumns+` FROM agents WHERE scope_id=? AND agent_id=?`, scopeID, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, Errorf(CodeNotFound, "Agent "+agentID+" was not found")
	}
	return agent, err
}

func (s *Store) Heartbeat(ctx context.Context, principal Principal, input HeartbeatInput) (Agent, bool, bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Agent{}, false, false, err
	}
	defer tx.Rollback()
	now := nowMillis()
	if err := requireCurrentExecution(ctx, tx, principal, now); err != nil {
		return Agent{}, false, false, err
	}
	var previousLifecycle AgentLifecycle
	var previousReady int
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle,ready FROM agents WHERE scope_id=? AND agent_id=? AND execution_id=?`, principal.ScopeID, principal.AgentID, principal.ExecutionID).
		Scan(&previousLifecycle, &previousReady); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Agent{}, false, false, Errorf(CodeUnauthenticated, "Agent execution has been replaced")
		}
		return Agent{}, false, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agents SET lifecycle=?,ready=?,lease_expires_at=?,updated_at=? WHERE scope_id=? AND agent_id=? AND execution_id=?`,
		input.Lifecycle, input.Ready, now+input.LeaseMS, now, principal.ScopeID, principal.AgentID, principal.ExecutionID)
	if err != nil {
		return Agent{}, false, false, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Agent{}, false, false, Errorf(CodeUnauthenticated, "Agent execution has been replaced")
	}
	stateChanged := previousLifecycle != input.Lifecycle || (previousReady == 1) != input.Ready
	becameReady := previousReady == 0 && input.Ready
	if stateChanged {
		if err := appendEvent(ctx, tx, principal.ScopeID, "agent.lifecycle_changed", principal.AgentID, eventAttributes(
			"executionId", principal.ExecutionID, "lifecycle", string(input.Lifecycle), "ready", fmt.Sprintf("%t", input.Ready), "leaseExpiresAt", instant(now+input.LeaseMS),
		), now); err != nil {
			return Agent{}, false, false, err
		}
	}
	agent, err := scanAgent(tx.QueryRowContext(ctx, `SELECT `+agentColumns+` FROM agents WHERE scope_id=? AND agent_id=?`, principal.ScopeID, principal.AgentID))
	if err != nil {
		return Agent{}, false, false, err
	}
	if err := tx.Commit(); err != nil {
		return Agent{}, false, false, err
	}
	return agent, stateChanged, becameReady, nil
}

func (s *Store) ListAgents(ctx context.Context, scopeID string) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentColumns+` FROM agents WHERE scope_id=? ORDER BY registered_at`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := []Agent{}
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

func orderedPeers(left, right string) (string, string) {
	if left < right {
		return left, right
	}
	return right, left
}

func linkAgents(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scopeID, left, right string, now int64) (bool, error) {
	if left == right {
		return false, Errorf(CodeInvalidArgument, "An agent cannot link to itself")
	}
	for _, id := range []string{left, right} {
		var found int
		err := executor.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE scope_id=? AND agent_id=?`, scopeID, id).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return false, Errorf(CodeNotFound, "Agent "+id+" was not found")
		}
		if err != nil {
			return false, err
		}
	}
	a, b := orderedPeers(left, right)
	result, err := executor.ExecContext(ctx, `INSERT OR IGNORE INTO peer_links(scope_id,left_agent,right_agent,created_at) VALUES(?,?,?,?)`, scopeID, a, b, now)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *Store) LinkAgents(ctx context.Context, scopeID, left, right string) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowMillis()
	created, err := linkAgents(ctx, tx, scopeID, left, right, now)
	if err != nil {
		return err
	}
	if created {
		a, b := orderedPeers(left, right)
		if err := appendEvent(ctx, tx, scopeID, "link.created", a+":"+b, eventAttributes("leftAgent", a, "rightAgent", b), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListPeers(ctx context.Context, principal Principal) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+"a."+strings.ReplaceAll(agentColumns, ",", ",a.")+` FROM peer_links l
JOIN agents a ON a.scope_id=l.scope_id AND a.agent_id=CASE WHEN l.left_agent=? THEN l.right_agent ELSE l.left_agent END
WHERE l.scope_id=? AND (l.left_agent=? OR l.right_agent=?) ORDER BY a.registered_at`,
		principal.AgentID, principal.ScopeID, principal.AgentID, principal.AgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	peers := []Agent{}
	for rows.Next() {
		peer, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		peers = append(peers, peer)
	}
	return peers, rows.Err()
}

func (s *Store) linked(ctx context.Context, tx *sql.Tx, scopeID, left, right string) (bool, error) {
	return linkedAgents(ctx, tx, scopeID, left, right)
}

const messageColumns = `message_id,scope_id,from_kind,from_id,to_kind,to_id,mode,body,context_json,response_to,state,created_at,expires_at,delivered_at,acknowledged_at,replied_at,response_message_id`

func scanMessage(row rowScanner) (Message, error) {
	var message Message
	var contextJSON string
	var responseTo, responseMessageID sql.NullString
	var expiresAt, deliveredAt, acknowledgedAt, repliedAt sql.NullInt64
	var createdAt int64
	err := row.Scan(&message.ID, &message.ScopeID, &message.FromKind, &message.From, &message.ToKind, &message.To, &message.Mode, &message.Body, &contextJSON,
		&responseTo, &message.State, &createdAt, &expiresAt, &deliveredAt, &acknowledgedAt, &repliedAt, &responseMessageID)
	if err != nil {
		return Message{}, err
	}
	if err := json.Unmarshal([]byte(contextJSON), &message.Context); err != nil {
		return Message{}, err
	}
	if message.Context == nil {
		message.Context = []ContextItem{}
	}
	message.ResponseTo = responseTo.String
	message.ResponseMessageID = responseMessageID.String
	message.CreatedAt = instant(createdAt)
	message.ExpiresAt = instant(expiresAt.Int64)
	message.DeliveredAt = instant(deliveredAt.Int64)
	message.AcknowledgedAt = instant(acknowledgedAt.Int64)
	message.RepliedAt = instant(repliedAt.Int64)
	return message, nil
}

func receiptFromMessage(message Message) DeliveryReceipt {
	return DeliveryReceipt{
		MessageID: message.ID, State: message.State, AcceptedAt: message.CreatedAt,
		DeliveredAt: message.DeliveredAt, AcknowledgedAt: message.AcknowledgedAt,
		RepliedAt: message.RepliedAt, ResponseMessageID: message.ResponseMessageID,
	}
}

func expireMessages(ctx context.Context, tx *sql.Tx, scopeID string, now int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT message_id,from_id,to_id,mode,delivered_at FROM messages WHERE scope_id=? AND expires_at IS NOT NULL AND expires_at<=? AND state NOT IN ('acknowledged','expired')`, scopeID, now)
	if err != nil {
		return err
	}
	type expiringMessage struct {
		id, from, to string
		mode         MessageMode
		deliveredAt  sql.NullInt64
	}
	values := []expiringMessage{}
	for rows.Next() {
		var value expiringMessage
		if err := rows.Scan(&value.id, &value.from, &value.to, &value.mode, &value.deliveredAt); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, value := range values {
		result, err := tx.ExecContext(ctx, `UPDATE messages SET state='expired',reservation_id=NULL WHERE message_id=? AND scope_id=? AND state NOT IN ('acknowledged','expired')`, value.id, scopeID)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed == 1 {
			if !value.deliveredAt.Valid {
				if err := transitionA2ATaskForMessage(ctx, tx, scopeID, value.id, func(state A2ATaskState) (A2ATaskState, bool) {
					return A2ATaskFailed, !a2aTaskTerminal(state)
				}, now); err != nil {
					return err
				}
			}
			if err := appendEvent(ctx, tx, scopeID, "message.expired", value.id, eventAttributes(
				"from", value.from, "to", value.to, "mode", string(value.mode), "state", string(DeliveryExpired),
			), now); err != nil {
				return err
			}
		}
	}
	return nil
}

func messageRequestHash(input SendMessageInput, mode MessageMode, contextJSON string) string {
	value := struct {
		To          string      `json:"to"`
		Body        string      `json:"body"`
		Mode        MessageMode `json:"mode"`
		ResponseTo  string      `json:"responseTo,omitempty"`
		ExpiresInMS int64       `json:"expiresInMs,omitempty"`
		ContextJSON string      `json:"context"`
	}{input.To, input.Body, mode, input.ResponseTo, input.ExpiresInMS, contextJSON}
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sendMessageTx(ctx context.Context, tx *sql.Tx, scopeID string, senderKind MessageParticipantKind, senderID string, input SendMessageInput) (Message, error) {
	mode := input.Mode
	if mode == "" {
		mode = MessageNotify
	}
	contextJSON, err := jsonValue(input.Context)
	if err != nil {
		return Message{}, err
	}
	requestHash := messageRequestHash(input, mode, contextJSON)
	toKind := MessageParticipantAgent
	now := nowMillis()
	if err := expireMessages(ctx, tx, scopeID, now); err != nil {
		return Message{}, err
	}
	if input.IdempotencyKey != "" {
		var existingID, existingHash string
		err := tx.QueryRowContext(ctx, `SELECT message_id,request_hash FROM messages WHERE scope_id=? AND from_kind=? AND from_id=? AND idempotency_key=?`,
			scopeID, senderKind, senderID, input.IdempotencyKey).Scan(&existingID, &existingHash)
		if err == nil {
			if existingHash != requestHash {
				return Message{}, Errorf(CodeConflict, "Idempotency key was already used with different message content")
			}
			message, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE message_id=?`, existingID))
			return message, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Message{}, err
		}
	}
	if mode == MessageResponse {
		if input.ResponseTo == "" {
			return Message{}, Errorf(CodeInvalidArgument, "responseTo is required for a response")
		}
		var requestFromKind, requestToKind MessageParticipantKind
		var requestFrom, requestTo string
		var state DeliveryState
		var deliveredAt, repliedAt sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT from_kind,from_id,to_kind,to_id,state,delivered_at,replied_at FROM messages WHERE message_id=? AND scope_id=? AND mode='request'`,
			input.ResponseTo, scopeID).Scan(&requestFromKind, &requestFrom, &requestToKind, &requestTo, &state, &deliveredAt, &repliedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return Message{}, Errorf(CodeNotFound, "The response request was not found for these participants")
		}
		if err != nil {
			return Message{}, err
		}
		if requestToKind != senderKind || requestTo != senderID || requestFrom != input.To {
			return Message{}, Errorf(CodeNotFound, "The response request was not found for these participants")
		}
		if !deliveredAt.Valid {
			if state == DeliveryExpired {
				return Message{}, Errorf(CodeConflict, "The request expired before delivery")
			}
			return Message{}, Errorf(CodeConflict, "The request has not been delivered")
		}
		if repliedAt.Valid {
			return Message{}, Errorf(CodeConflict, "The request already has a response")
		}
		toKind = requestFromKind
	} else if input.ResponseTo != "" {
		return Message{}, Errorf(CodeInvalidArgument, "responseTo is valid only for a response")
	}
	if senderKind == MessageParticipantAgent && toKind == MessageParticipantAgent {
		linked, err := linkedAgents(ctx, tx, scopeID, senderID, input.To)
		if err != nil {
			return Message{}, err
		}
		if !linked {
			return Message{}, Errorf(CodePermissionDenied, "Agent "+input.To+" is not a linked peer")
		}
	} else if toKind == MessageParticipantAgent {
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE scope_id=? AND agent_id=?`, scopeID, input.To).Scan(&found); errors.Is(err, sql.ErrNoRows) {
			return Message{}, Errorf(CodeNotFound, "Agent "+input.To+" was not found")
		} else if err != nil {
			return Message{}, err
		}
	}
	var backlog int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE scope_id=? AND state NOT IN ('acknowledged','expired')`, scopeID).Scan(&backlog); err != nil {
		return Message{}, err
	}
	if backlog >= messageBacklogCap {
		return Message{}, Errorf(CodeBackpressure, "Scope message backlog is full")
	}
	messageID, err := randomID("msg_")
	if err != nil {
		return Message{}, err
	}
	initialState := DeliveryQueued
	var deliveredAt, acknowledgedAt any
	if toKind == MessageParticipantA2APrincipal {
		initialState = DeliveryAcknowledged
		deliveredAt, acknowledgedAt = now, now
	}
	if err := appendEvent(ctx, tx, scopeID, "message.accepted", messageID, eventAttributes(
		"from", senderID, "fromKind", string(senderKind), "to", input.To, "toKind", string(toKind), "mode", string(mode), "state", string(initialState), "responseTo", input.ResponseTo,
	), now); err != nil {
		return Message{}, err
	}
	var expiresAt any
	if input.ExpiresInMS > 0 {
		expiresAt = now + input.ExpiresInMS
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO messages(message_id,scope_id,from_kind,from_id,to_kind,to_id,mode,body,context_json,response_to,idempotency_key,request_hash,state,created_at,expires_at,delivered_at,acknowledged_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		messageID, scopeID, senderKind, senderID, toKind, input.To, mode, input.Body, contextJSON, nullableString(input.ResponseTo), nullableString(input.IdempotencyKey), requestHash, initialState, now, expiresAt, deliveredAt, acknowledgedAt)
	if err != nil {
		return Message{}, err
	}
	if mode == MessageResponse {
		result, err := tx.ExecContext(ctx, `UPDATE messages SET replied_at=?,response_message_id=? WHERE message_id=? AND scope_id=? AND from_kind=? AND from_id=? AND to_kind=? AND to_id=? AND replied_at IS NULL`,
			now, messageID, input.ResponseTo, scopeID, toKind, input.To, senderKind, senderID)
		if err != nil {
			return Message{}, err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return Message{}, Errorf(CodeConflict, "The request already has a response")
		}
		if err := appendEvent(ctx, tx, scopeID, "message.replied", input.ResponseTo, eventAttributes(
			"responseMessageId", messageID, "from", input.To, "to", senderID,
		), now); err != nil {
			return Message{}, err
		}
		if toKind == MessageParticipantA2APrincipal {
			if _, err := tx.ExecContext(ctx, `UPDATE a2a_message_correlations SET bus_response_message_id=?,updated_at=? WHERE bus_request_message_id=?`, messageID, now, input.ResponseTo); err != nil {
				return Message{}, err
			}
			if err := transitionA2ATaskForMessage(ctx, tx, scopeID, input.ResponseTo, func(state A2ATaskState) (A2ATaskState, bool) {
				return A2ATaskCompleted, state != A2ATaskInputRequired && !a2aTaskTerminal(state)
			}, now); err != nil {
				return Message{}, err
			}
		}
	}
	message, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE message_id=?`, messageID))
	return message, err
}

func linkedAgents(ctx context.Context, tx *sql.Tx, scopeID, left, right string) (bool, error) {
	a, b := orderedPeers(left, right)
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM peer_links WHERE scope_id=? AND left_agent=? AND right_agent=?`, scopeID, a, b).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) SendMessage(ctx context.Context, principal Principal, input SendMessageInput) (DeliveryReceipt, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, principal, nowMillis()); err != nil {
		return DeliveryReceipt{}, err
	}
	message, err := sendMessageTx(ctx, tx, principal.ScopeID, MessageParticipantAgent, principal.AgentID, input)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeliveryReceipt{}, err
	}
	return receiptFromMessage(message), nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) Receipt(ctx context.Context, principal Principal, messageID string) (DeliveryReceipt, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, principal, nowMillis()); err != nil {
		return DeliveryReceipt{}, err
	}
	if err := expireMessages(ctx, tx, principal.ScopeID, nowMillis()); err != nil {
		return DeliveryReceipt{}, err
	}
	message, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE message_id=? AND scope_id=? AND ((from_kind='agent' AND from_id=?) OR (to_kind='agent' AND to_id=?))`,
		messageID, principal.ScopeID, principal.AgentID, principal.AgentID))
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryReceipt{}, Errorf(CodeNotFound, "Message "+messageID+" was not found")
	}
	if err != nil {
		return DeliveryReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeliveryReceipt{}, err
	}
	return receiptFromMessage(message), nil
}

func releaseReservation(ctx context.Context, tx *sql.Tx, scopeID, agentID, reservationID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT message_id,from_id,to_id,mode,CASE WHEN delivered_at IS NULL THEN 'queued' ELSE 'delivered' END FROM messages WHERE reservation_id=? AND scope_id=? AND to_kind='agent' AND to_id=? AND state='reserved'`, reservationID, scopeID, agentID)
	if err != nil {
		return err
	}
	type releasedMessage struct {
		id, from, to string
		mode         MessageMode
		state        DeliveryState
	}
	values := []releasedMessage{}
	for rows.Next() {
		var value releasedMessage
		if err := rows.Scan(&value.id, &value.from, &value.to, &value.mode, &value.state); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET state=CASE WHEN delivered_at IS NULL THEN 'queued' ELSE 'delivered' END,reservation_id=NULL WHERE reservation_id=? AND scope_id=? AND to_kind='agent' AND to_id=? AND state='reserved'`, reservationID, scopeID, agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reservations WHERE reservation_id=? AND scope_id=? AND agent_id=?`, reservationID, scopeID, agentID); err != nil {
		return err
	}
	now := nowMillis()
	for _, value := range values {
		if err := appendEvent(ctx, tx, scopeID, "message.released", value.id, eventAttributes(
			"from", value.from, "to", value.to, "mode", string(value.mode), "state", string(value.state),
		), now); err != nil {
			return err
		}
	}
	return nil
}

func requireCurrentExecution(ctx context.Context, tx *sql.Tx, principal Principal, now int64) error {
	var executionID string
	var leaseExpiresAt int64
	err := tx.QueryRowContext(ctx, `SELECT execution_id,lease_expires_at FROM agents WHERE scope_id=? AND agent_id=?`, principal.ScopeID, principal.AgentID).Scan(&executionID, &leaseExpiresAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (executionID != principal.ExecutionID || leaseExpiresAt <= now)) {
		return Errorf(CodeUnauthenticated, "Agent execution is no longer current")
	}
	return err
}

func (s *Store) ReserveInbox(ctx context.Context, principal Principal, limit int) (*InboxReservation, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowMillis()
	if err := requireCurrentExecution(ctx, tx, principal, now); err != nil {
		return nil, err
	}
	if err := expireMessages(ctx, tx, principal.ScopeID, now); err != nil {
		return nil, err
	}
	expired, err := tx.QueryContext(ctx, `SELECT reservation_id,scope_id,agent_id FROM reservations WHERE expires_at<=?`, now)
	if err != nil {
		return nil, err
	}
	type expiredReservation struct{ id, scopeID, agentID string }
	var expiredValues []expiredReservation
	for expired.Next() {
		var value expiredReservation
		if err := expired.Scan(&value.id, &value.scopeID, &value.agentID); err != nil {
			expired.Close()
			return nil, err
		}
		expiredValues = append(expiredValues, value)
	}
	expired.Close()
	for _, value := range expiredValues {
		if err := releaseReservation(ctx, tx, value.scopeID, value.agentID, value.id); err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE scope_id=? AND to_kind='agent' AND to_id=? AND state IN ('queued','delivered') AND (expires_at IS NULL OR expires_at>?) ORDER BY created_at LIMIT ?`,
		principal.ScopeID, principal.AgentID, now, limit)
	if err != nil {
		return nil, err
	}
	messages := []Message{}
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		messages = append(messages, message)
	}
	rows.Close()
	if len(messages) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	reservationID, err := randomID("res_")
	if err != nil {
		return nil, err
	}
	expiresAt := now + reservationTTL.Milliseconds()
	if _, err := tx.ExecContext(ctx, `INSERT INTO reservations(reservation_id,scope_id,agent_id,created_at,expires_at) VALUES(?,?,?,?,?)`, reservationID, principal.ScopeID, principal.AgentID, now, expiresAt); err != nil {
		return nil, err
	}
	for i := range messages {
		result, err := tx.ExecContext(ctx, `UPDATE messages SET state='reserved',reservation_id=? WHERE message_id=? AND scope_id=? AND to_kind='agent' AND to_id=? AND state IN ('queued','delivered')`,
			reservationID, messages[i].ID, principal.ScopeID, principal.AgentID)
		if err != nil {
			return nil, err
		}
		changed, _ := result.RowsAffected()
		if changed == 1 {
			messages[i].State = DeliveryReserved
			if err := appendEvent(ctx, tx, principal.ScopeID, "message.reserved", messages[i].ID, eventAttributes(
				"from", messages[i].From, "to", messages[i].To, "mode", string(messages[i].Mode), "state", string(DeliveryReserved),
			), now); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &InboxReservation{ID: reservationID, ExpiresAt: instant(expiresAt), Messages: messages}, nil
}

func (s *Store) NextInboxReservationExpiry(ctx context.Context, principal Principal) (int64, error) {
	var expiresAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MIN(expires_at) FROM reservations WHERE scope_id=? AND agent_id=?`, principal.ScopeID, principal.AgentID).Scan(&expiresAt)
	if err != nil {
		return 0, err
	}
	return expiresAt.Int64, nil
}

func (s *Store) CommitInbox(ctx context.Context, principal Principal, reservationID string) ([]Message, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, principal, nowMillis()); err != nil {
		return nil, err
	}
	var expiresAt int64
	err = tx.QueryRowContext(ctx, `SELECT expires_at FROM reservations WHERE reservation_id=? AND scope_id=? AND agent_id=?`, reservationID, principal.ScopeID, principal.AgentID).Scan(&expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Errorf(CodeNotFound, "Inbox reservation was not found")
	}
	if err != nil {
		return nil, err
	}
	if expiresAt <= nowMillis() {
		if err := releaseReservation(ctx, tx, principal.ScopeID, principal.AgentID, reservationID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, Errorf(CodeConflict, "Inbox reservation expired")
	}
	if err := expireMessages(ctx, tx, principal.ScopeID, nowMillis()); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE reservation_id=? AND scope_id=? AND to_kind='agent' AND to_id=? AND state='reserved' ORDER BY created_at`, reservationID, principal.ScopeID, principal.AgentID)
	if err != nil {
		return nil, err
	}
	messages := []Message{}
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		messages = append(messages, message)
	}
	rows.Close()
	deliveredAt := nowMillis()
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET state='delivered',delivered_at=COALESCE(delivered_at,?),reservation_id=NULL WHERE reservation_id=? AND scope_id=? AND to_kind='agent' AND to_id=? AND state='reserved'`,
		deliveredAt, reservationID, principal.ScopeID, principal.AgentID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reservations WHERE reservation_id=?`, reservationID); err != nil {
		return nil, err
	}
	for _, message := range messages {
		if message.FromKind == MessageParticipantA2APrincipal {
			if err := transitionA2ATaskForMessage(ctx, tx, principal.ScopeID, message.ID, func(state A2ATaskState) (A2ATaskState, bool) {
				return A2ATaskWorking, state == A2ATaskSubmitted
			}, deliveredAt); err != nil {
				return nil, err
			}
		}
		if err := appendEvent(ctx, tx, principal.ScopeID, "message.delivered", message.ID, eventAttributes(
			"from", message.From, "to", message.To, "mode", string(message.Mode), "state", string(DeliveryDelivered),
		), deliveredAt); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for i := range messages {
		messages[i].State = DeliveryDelivered
		if messages[i].DeliveredAt == "" {
			messages[i].DeliveredAt = instant(deliveredAt)
		}
	}
	return messages, nil
}

func (s *Store) ReleaseInbox(ctx context.Context, principal Principal, reservationID string) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, principal, nowMillis()); err != nil {
		return err
	}
	if err := expireMessages(ctx, tx, principal.ScopeID, nowMillis()); err != nil {
		return err
	}
	if err := releaseReservation(ctx, tx, principal.ScopeID, principal.AgentID, reservationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AcknowledgeMessages(ctx context.Context, principal Principal, messageIDs []string) (int64, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, principal, nowMillis()); err != nil {
		return 0, err
	}
	if err := expireMessages(ctx, tx, principal.ScopeID, nowMillis()); err != nil {
		return 0, err
	}
	var count int64
	for _, messageID := range unique(messageIDs) {
		now := nowMillis()
		result, err := tx.ExecContext(ctx, `UPDATE messages SET state='acknowledged',acknowledged_at=? WHERE message_id=? AND scope_id=? AND to_kind='agent' AND to_id=? AND state='delivered'`,
			now, messageID, principal.ScopeID, principal.AgentID)
		if err != nil {
			return 0, err
		}
		changed, _ := result.RowsAffected()
		count += changed
		if changed == 1 {
			var from, to string
			var mode MessageMode
			if err := tx.QueryRowContext(ctx, `SELECT from_id,to_id,mode FROM messages WHERE message_id=?`, messageID).Scan(&from, &to, &mode); err != nil {
				return 0, err
			}
			if err := appendEvent(ctx, tx, principal.ScopeID, "message.acknowledged", messageID, eventAttributes(
				"from", from, "to", to, "mode", string(mode), "state", string(DeliveryAcknowledged),
			), now); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

const taskColumns = `task_id,scope_id,title,description,created_by,claimed_by,status,dependencies_json,note,created_at,updated_at`

func scanTask(row rowScanner) (Task, error) {
	var task Task
	var createdBy, claimedBy, note sql.NullString
	var dependencies string
	var createdAt, updatedAt int64
	err := row.Scan(&task.ID, &task.ScopeID, &task.Title, &task.Description, &createdBy, &claimedBy, &task.Status, &dependencies, &note, &createdAt, &updatedAt)
	if err != nil {
		return Task{}, err
	}
	if err := json.Unmarshal([]byte(dependencies), &task.Dependencies); err != nil {
		return Task{}, err
	}
	if task.Dependencies == nil {
		task.Dependencies = []string{}
	}
	task.RecentProgress = []TaskProgress{}
	if createdBy.Valid {
		task.CreatedBy = &createdBy.String
	}
	task.ClaimedBy = claimedBy.String
	task.Note = note.String
	task.CreatedAt = instant(createdAt)
	task.UpdatedAt = instant(updatedAt)
	return task, nil
}

func taskFrom(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, scopeID, taskID string) (Task, error) {
	task, err := scanTask(query.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE scope_id=? AND task_id=?`, scopeID, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, Errorf(CodeNotFound, "Task "+taskID+" was not found")
	}
	if err != nil {
		return task, err
	}
	if task.Status == "open" {
		task.Ready = true
		for _, dependency := range task.Dependencies {
			var status string
			depErr := query.QueryRowContext(ctx, `SELECT status FROM tasks WHERE scope_id=? AND task_id=?`, scopeID, dependency).Scan(&status)
			if depErr != nil {
				return Task{}, depErr
			}
			if status != "done" {
				task.Ready = false
				break
			}
		}
	}
	task.RecentProgress, err = recentProgressForTask(ctx, query, scopeID, taskID)
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

func releaseStaleTaskClaims(ctx context.Context, tx *sql.Tx, scopeID string) error {
	now := nowMillis()
	rows, err := tx.QueryContext(ctx, `SELECT task_id,claimed_by FROM tasks WHERE scope_id=? AND status='claimed' AND NOT EXISTS (
  SELECT 1 FROM agents
  WHERE agents.scope_id=tasks.scope_id
    AND agents.agent_id=tasks.claimed_by
    AND agents.execution_id=tasks.claimed_execution_id
    AND agents.lease_expires_at>?
) ORDER BY task_id`, scopeID, now)
	if err != nil {
		return err
	}
	type staleTask struct{ id, claimedBy string }
	stale := []staleTask{}
	for rows.Next() {
		var taskID, claimedBy string
		if err := rows.Scan(&taskID, &claimedBy); err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, staleTask{id: taskID, claimedBy: claimedBy})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, task := range stale {
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET status='open',claimed_by=NULL,claimed_execution_id=NULL,updated_at=? WHERE scope_id=? AND task_id=? AND status='claimed'`, now, scopeID, task.id)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed == 1 {
			if err := appendEvent(ctx, tx, scopeID, "task.released", task.id, eventAttributes("previousClaimedBy", task.claimedBy, "reason", "execution_expired", "status", "open"), now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) AddTask(ctx context.Context, scopeID, createdBy string, input AddTaskInput) (Task, error) {
	return s.addTask(ctx, scopeID, createdBy, nil, input)
}

func (s *Store) AddAgentTask(ctx context.Context, principal Principal, input AddTaskInput) (Task, error) {
	return s.addTask(ctx, principal.ScopeID, principal.AgentID, &principal, input)
}

func (s *Store) addTask(ctx context.Context, scopeID, createdBy string, principal *Principal, input AddTaskInput) (Task, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	if principal != nil {
		if err := requireCurrentExecution(ctx, tx, *principal, nowMillis()); err != nil {
			return Task{}, err
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE scope_id=? AND status!='done'`, scopeID).Scan(&count); err != nil {
		return Task{}, err
	}
	if count >= activeTaskCap {
		return Task{}, Errorf(CodeBackpressure, "Scope active task limit is full")
	}
	dependencies := unique(input.Dependencies)
	for _, dependency := range dependencies {
		var found int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE scope_id=? AND task_id=?`, scopeID, dependency).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, Errorf(CodeNotFound, "Dependency "+dependency+" was not found")
		}
		if err != nil {
			return Task{}, err
		}
	}
	taskID, err := randomID("task_")
	if err != nil {
		return Task{}, err
	}
	dependenciesJSON, err := jsonValue(dependencies)
	if err != nil {
		return Task{}, err
	}
	now := nowMillis()
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(task_id,scope_id,title,description,created_by,claimed_by,status,dependencies_json,note,created_at,updated_at) VALUES(?,?,?,?,?,NULL,'open',?,NULL,?,?)`,
		taskID, scopeID, input.Title, input.Description, nullableString(createdBy), dependenciesJSON, now, now)
	if err != nil {
		return Task{}, err
	}
	if err := appendEvent(ctx, tx, scopeID, "task.created", taskID, eventAttributes("createdBy", createdBy, "status", "open"), now); err != nil {
		return Task{}, err
	}
	task, err := taskFrom(ctx, tx, scopeID, taskID)
	if err != nil {
		return Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Store) ClaimTask(ctx context.Context, principal Principal, taskID string) (Task, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, principal, nowMillis()); err != nil {
		return Task{}, err
	}
	if err := releaseStaleTaskClaims(ctx, tx, principal.ScopeID); err != nil {
		return Task{}, err
	}
	task, err := taskFrom(ctx, tx, principal.ScopeID, taskID)
	if err != nil {
		return Task{}, err
	}
	if task.Status != "open" {
		return Task{}, Errorf(CodeConflict, "Task "+taskID+" is not open")
	}
	for _, dependency := range task.Dependencies {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE scope_id=? AND task_id=?`, principal.ScopeID, dependency).Scan(&status)
		if err != nil || status != "done" {
			return Task{}, Errorf(CodeConflict, "Task "+taskID+" is blocked by "+dependency)
		}
	}
	now := nowMillis()
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET status='claimed',claimed_by=?,claimed_execution_id=?,updated_at=? WHERE task_id=? AND scope_id=? AND status='open'`,
		principal.AgentID, principal.ExecutionID, now, taskID, principal.ScopeID)
	if err != nil {
		return Task{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Task{}, Errorf(CodeConflict, "Task "+taskID+" was claimed by another agent")
	}
	if err := appendEvent(ctx, tx, principal.ScopeID, "task.claimed", taskID, eventAttributes(
		"claimedBy", principal.AgentID, "executionId", principal.ExecutionID, "status", "claimed",
	), now); err != nil {
		return Task{}, err
	}
	task, err = taskFrom(ctx, tx, principal.ScopeID, taskID)
	if err != nil {
		return Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Store) CompleteTask(ctx context.Context, principal Principal, taskID, note string) (Task, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, principal, nowMillis()); err != nil {
		return Task{}, err
	}
	now := nowMillis()
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET status='done',note=?,updated_at=? WHERE task_id=? AND scope_id=? AND status='claimed' AND claimed_by=? AND claimed_execution_id=?`,
		nullableString(note), now, taskID, principal.ScopeID, principal.AgentID, principal.ExecutionID)
	if err != nil {
		return Task{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Task{}, Errorf(CodeConflict, "Task "+taskID+" is not claimed by this agent")
	}
	if err := appendEvent(ctx, tx, principal.ScopeID, "task.completed", taskID, eventAttributes(
		"claimedBy", principal.AgentID, "executionId", principal.ExecutionID, "status", "done",
	), now); err != nil {
		return Task{}, err
	}
	task, err := taskFrom(ctx, tx, principal.ScopeID, taskID)
	if err != nil {
		return Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Store) ReleaseTask(ctx context.Context, principal Principal, taskID string) (Task, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, principal, nowMillis()); err != nil {
		return Task{}, err
	}
	now := nowMillis()
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET status='open',claimed_by=NULL,claimed_execution_id=NULL,updated_at=? WHERE task_id=? AND scope_id=? AND status='claimed' AND claimed_by=? AND claimed_execution_id=?`,
		now, taskID, principal.ScopeID, principal.AgentID, principal.ExecutionID)
	if err != nil {
		return Task{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Task{}, Errorf(CodeConflict, "Task "+taskID+" is not claimed by this execution")
	}
	if err := appendEvent(ctx, tx, principal.ScopeID, "task.released", taskID, eventAttributes(
		"previousClaimedBy", principal.AgentID, "executionId", principal.ExecutionID, "reason", "released", "status", "open",
	), now); err != nil {
		return Task{}, err
	}
	task, err := taskFrom(ctx, tx, principal.ScopeID, taskID)
	if err != nil {
		return Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Store) ListTasks(ctx context.Context, scopeID string, readyOnly bool) ([]Task, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var total int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE scope_id=?", scopeID).Scan(&total); err != nil {
		return nil, err
	}
	if total > maxListedTasks {
		return nil, Errorf(CodeBackpressure, "Task history exceeds 10000 records; use /v1/tasks/page")
	}
	if err := releaseStaleTaskClaims(ctx, tx, scopeID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE scope_id=? ORDER BY created_at`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	progress, err := recentProgressForScope(ctx, tx, scopeID)
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]string, len(tasks))
	for _, task := range tasks {
		statuses[task.ID] = task.Status
	}
	filtered := make([]Task, 0, len(tasks))
	for i := range tasks {
		tasks[i].RecentProgress = progress[tasks[i].ID]
		if tasks[i].RecentProgress == nil {
			tasks[i].RecentProgress = []TaskProgress{}
		}
		if tasks[i].Status == "open" {
			tasks[i].Ready = true
			for _, dependency := range tasks[i].Dependencies {
				if statuses[dependency] != "done" {
					tasks[i].Ready = false
					break
				}
			}
		}
		if !readyOnly || tasks[i].Ready {
			filtered = append(filtered, tasks[i])
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return filtered, nil
}

const escalationColumns = `escalation_id,scope_id,agent_id,question,options_json,status,answer,created_at,resolved_at`

func scanEscalation(row rowScanner) (HumanEscalation, error) {
	var escalation HumanEscalation
	var options string
	var answer sql.NullString
	var createdAt int64
	var resolvedAt sql.NullInt64
	err := row.Scan(&escalation.ID, &escalation.ScopeID, &escalation.AgentID, &escalation.Question, &options, &escalation.Status, &answer, &createdAt, &resolvedAt)
	if err != nil {
		return HumanEscalation{}, err
	}
	if err := json.Unmarshal([]byte(options), &escalation.Options); err != nil {
		return HumanEscalation{}, err
	}
	if escalation.Options == nil {
		escalation.Options = []string{}
	}
	escalation.Answer = answer.String
	escalation.CreatedAt = instant(createdAt)
	escalation.ResolvedAt = instant(resolvedAt.Int64)
	return escalation, nil
}

func escalationFrom(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scopeID, escalationID string) (HumanEscalation, error) {
	escalation, err := scanEscalation(query.QueryRowContext(ctx, `SELECT `+escalationColumns+` FROM escalations WHERE scope_id=? AND escalation_id=?`, scopeID, escalationID))
	if errors.Is(err, sql.ErrNoRows) {
		return HumanEscalation{}, Errorf(CodeNotFound, "Escalation "+escalationID+" was not found")
	}
	return escalation, err
}

func (s *Store) AskHuman(ctx context.Context, principal Principal, input AskHumanInput) (HumanEscalation, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return HumanEscalation{}, err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, principal, nowMillis()); err != nil {
		return HumanEscalation{}, err
	}
	var agentPending, scopePending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM escalations WHERE scope_id=? AND agent_id=? AND status='pending'`, principal.ScopeID, principal.AgentID).Scan(&agentPending); err != nil {
		return HumanEscalation{}, err
	}
	if agentPending >= pendingEscalationCapPerAgent {
		return HumanEscalation{}, Errorf(CodeBackpressure, "Agent pending escalation limit is full")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM escalations WHERE scope_id=? AND status='pending'`, principal.ScopeID).Scan(&scopePending); err != nil {
		return HumanEscalation{}, err
	}
	if scopePending >= pendingEscalationCapPerScope {
		return HumanEscalation{}, Errorf(CodeBackpressure, "Scope pending escalation limit is full")
	}
	id, err := randomID("ask_")
	if err != nil {
		return HumanEscalation{}, err
	}
	options, err := jsonValue(input.Options)
	if err != nil {
		return HumanEscalation{}, err
	}
	now := nowMillis()
	_, err = tx.ExecContext(ctx, `INSERT INTO escalations(escalation_id,scope_id,agent_id,question,options_json,status,answer,created_at,resolved_at) VALUES(?,?,?,?,?,'pending',NULL,?,NULL)`,
		id, principal.ScopeID, principal.AgentID, input.Question, options, now)
	if err != nil {
		return HumanEscalation{}, err
	}
	if err := appendEvent(ctx, tx, principal.ScopeID, "escalation.created", id, eventAttributes("agentId", principal.AgentID, "status", "pending"), now); err != nil {
		return HumanEscalation{}, err
	}
	escalation, err := escalationFrom(ctx, tx, principal.ScopeID, id)
	if err != nil {
		return HumanEscalation{}, err
	}
	if err := tx.Commit(); err != nil {
		return HumanEscalation{}, err
	}
	return escalation, nil
}

func (s *Store) Escalation(ctx context.Context, scopeID, escalationID string) (HumanEscalation, error) {
	return escalationFrom(ctx, s.db, scopeID, escalationID)
}

func (s *Store) ListEscalations(ctx context.Context, scopeID string) ([]HumanEscalation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+escalationColumns+` FROM escalations WHERE scope_id=? ORDER BY created_at`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	escalations := []HumanEscalation{}
	for rows.Next() {
		escalation, err := scanEscalation(rows)
		if err != nil {
			return nil, err
		}
		escalations = append(escalations, escalation)
	}
	return escalations, rows.Err()
}

func (s *Store) ResolveEscalation(ctx context.Context, scopeID, escalationID, answer string) (HumanEscalation, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return HumanEscalation{}, err
	}
	defer tx.Rollback()
	now := nowMillis()
	result, err := tx.ExecContext(ctx, `UPDATE escalations SET status='resolved',answer=?,resolved_at=? WHERE scope_id=? AND escalation_id=? AND status='pending'`,
		answer, now, scopeID, escalationID)
	if err != nil {
		return HumanEscalation{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		// Distinguish an unknown escalation (NOT_FOUND) from one that was already resolved (CONFLICT).
		if _, err := escalationFrom(ctx, tx, scopeID, escalationID); err != nil {
			return HumanEscalation{}, err
		}
		return HumanEscalation{}, Errorf(CodeConflict, "Escalation is already resolved")
	}
	var agentID string
	if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM escalations WHERE scope_id=? AND escalation_id=?`, scopeID, escalationID).Scan(&agentID); err != nil {
		return HumanEscalation{}, err
	}
	if err := appendEvent(ctx, tx, scopeID, "escalation.resolved", escalationID, eventAttributes("agentId", agentID, "status", "resolved"), now); err != nil {
		return HumanEscalation{}, err
	}
	escalation, err := escalationFrom(ctx, tx, scopeID, escalationID)
	if err != nil {
		return HumanEscalation{}, err
	}
	if err := tx.Commit(); err != nil {
		return HumanEscalation{}, err
	}
	return escalation, nil
}
