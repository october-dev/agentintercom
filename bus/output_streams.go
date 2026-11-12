package bus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
)

const (
	defaultOutputRetention = 1000
	maxOutputRetention     = 10000
	maxOutputStreams       = 1000
	maxOutputPublishers    = 128
	maxOutputTextBytes     = 64 * 1024
	maxOutputJSONBytes     = 256 * 1024
	outputPublishRate      = 120
	outputReadRate         = 600
	defaultOutputLimit     = 50
	maxOutputLimit         = 100
)

type outputStreamRecord struct {
	ID              string
	ScopeID         string
	Name            string
	RetentionLimit  int
	CurrentSequence int64
	MinimumCursor   int64
	CreatedAt       int64
	UpdatedAt       int64
}

const outputStreamColumns = `stream_id,scope_id,name,retention_limit,sequence,floor_sequence,created_at,updated_at`

func scanOutputStream(row rowScanner) (outputStreamRecord, error) {
	var stream outputStreamRecord
	err := row.Scan(&stream.ID, &stream.ScopeID, &stream.Name, &stream.RetentionLimit, &stream.CurrentSequence, &stream.MinimumCursor, &stream.CreatedAt, &stream.UpdatedAt)
	return stream, err
}

func outputStreamFrom(record outputStreamRecord, publishers []string) OutputStream {
	if publishers == nil {
		publishers = []string{}
	}
	return OutputStream{
		ID: record.ID, ScopeID: record.ScopeID, Name: record.Name, RetentionLimit: record.RetentionLimit,
		CurrentSequence: record.CurrentSequence, MinimumCursor: record.MinimumCursor,
		PublisherAgentIDs: publishers, CreatedAt: instant(record.CreatedAt), UpdatedAt: instant(record.UpdatedAt),
	}
}

func outputPublishers(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, scopeID string) (map[string][]string, error) {
	rows, err := query.QueryContext(ctx, `SELECT stream_id,agent_id FROM output_stream_publishers WHERE scope_id=? ORDER BY stream_id,agent_id`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]string{}
	for rows.Next() {
		var streamID, agentID string
		if err := rows.Scan(&streamID, &agentID); err != nil {
			return nil, err
		}
		result[streamID] = append(result[streamID], agentID)
	}
	return result, rows.Err()
}

func outputStreamFromQuery(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, scopeID, streamID string) (OutputStream, error) {
	record, err := scanOutputStream(query.QueryRowContext(ctx, `SELECT `+outputStreamColumns+` FROM output_streams WHERE scope_id=? AND stream_id=?`, scopeID, streamID))
	if errors.Is(err, sql.ErrNoRows) {
		return OutputStream{}, Errorf(CodeNotFound, "Output stream "+streamID+" was not found")
	}
	if err != nil {
		return OutputStream{}, err
	}
	publishers, err := outputPublishers(ctx, query, scopeID)
	if err != nil {
		return OutputStream{}, err
	}
	return outputStreamFrom(record, publishers[record.ID]), nil
}

func (s *Store) CreateOutputStream(ctx context.Context, scopeID string, input CreateOutputStreamInput) (OutputStream, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return OutputStream{}, err
	}
	defer tx.Rollback()
	if err := requireHostedNewWork(ctx, tx, scopeID); err != nil {
		return OutputStream{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM output_streams WHERE scope_id=?`, scopeID).Scan(&count); err != nil {
		return OutputStream{}, err
	}
	if count >= maxOutputStreams {
		return OutputStream{}, Errorf(CodeBackpressure, "Scope output stream limit is full")
	}
	for _, agentID := range input.PublisherAgentIDs {
		var found int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE scope_id=? AND agent_id=?`, scopeID, agentID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return OutputStream{}, Errorf(CodeNotFound, "Agent "+agentID+" was not found")
		}
		if err != nil {
			return OutputStream{}, err
		}
	}
	streamID, err := randomID("out_")
	if err != nil {
		return OutputStream{}, err
	}
	now := nowMillis()
	_, err = tx.ExecContext(ctx, `INSERT INTO output_streams(stream_id,scope_id,name,retention_limit,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		streamID, scopeID, input.Name, input.RetentionLimit, now, now)
	if err != nil {
		if isStorageConstraint(err) {
			return OutputStream{}, Errorf(CodeConflict, "Output stream name already exists")
		}
		return OutputStream{}, err
	}
	for _, agentID := range input.PublisherAgentIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO output_stream_publishers(stream_id,scope_id,agent_id) VALUES(?,?,?)`, streamID, scopeID, agentID); err != nil {
			return OutputStream{}, err
		}
	}
	if err := appendEvent(ctx, tx, scopeID, "output.stream_created", streamID, eventAttributes(
		"retentionLimit", fmt.Sprintf("%d", input.RetentionLimit),
	), now); err != nil {
		return OutputStream{}, err
	}
	stream, err := outputStreamFromQuery(ctx, tx, scopeID, streamID)
	if err != nil {
		return OutputStream{}, err
	}
	if err := tx.Commit(); err != nil {
		return OutputStream{}, err
	}
	return stream, nil
}

func (s *Store) ListOutputStreams(ctx context.Context, scopeID string) ([]OutputStream, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+outputStreamColumns+` FROM output_streams WHERE scope_id=? ORDER BY created_at,stream_id`, scopeID)
	if err != nil {
		return nil, err
	}
	records := []outputStreamRecord{}
	for rows.Next() {
		record, err := scanOutputStream(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	publishers, err := outputPublishers(ctx, tx, scopeID)
	if err != nil {
		return nil, err
	}
	streams := make([]OutputStream, 0, len(records))
	for _, record := range records {
		streams = append(streams, outputStreamFrom(record, publishers[record.ID]))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return streams, nil
}

func (s *Store) OutputStream(ctx context.Context, scopeID, streamID string) (OutputStream, error) {
	return outputStreamFromQuery(ctx, s.db, scopeID, streamID)
}

func (s *Store) RemoveOutputStream(ctx context.Context, scopeID, streamID string) error {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM output_streams WHERE scope_id=? AND stream_id=?`, scopeID, streamID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return Errorf(CodeNotFound, "Output stream "+streamID+" was not found")
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM scoped_credentials WHERE credential_id IN (
		SELECT credential_id FROM scoped_credential_grants WHERE resource_type=? AND resource_id=?
	)`, outputStreamResource, streamID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM output_streams WHERE scope_id=? AND stream_id=?`, scopeID, streamID); err != nil {
		return err
	}
	now := nowMillis()
	if err := appendEvent(ctx, tx, scopeID, "output.stream_removed", streamID, nil, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetOutputPublisher(ctx context.Context, scopeID, streamID, agentID string, allowed bool) (OutputStream, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return OutputStream{}, err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM output_streams WHERE scope_id=? AND stream_id=?`, scopeID, streamID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return OutputStream{}, Errorf(CodeNotFound, "Output stream "+streamID+" was not found")
	} else if err != nil {
		return OutputStream{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE scope_id=? AND agent_id=?`, scopeID, agentID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return OutputStream{}, Errorf(CodeNotFound, "Agent "+agentID+" was not found")
	} else if err != nil {
		return OutputStream{}, err
	}
	now := nowMillis()
	changed := false
	if allowed {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM output_stream_publishers WHERE stream_id=? AND agent_id=?`, streamID, agentID).Scan(&exists)
		if err == nil {
			stream, err := outputStreamFromQuery(ctx, tx, scopeID, streamID)
			if err != nil {
				return OutputStream{}, err
			}
			if err := tx.Commit(); err != nil {
				return OutputStream{}, err
			}
			return stream, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return OutputStream{}, err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM output_stream_publishers WHERE stream_id=?`, streamID).Scan(&count); err != nil {
			return OutputStream{}, err
		}
		if count >= maxOutputPublishers {
			return OutputStream{}, Errorf(CodeBackpressure, "Output publisher limit is full")
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO output_stream_publishers(stream_id,scope_id,agent_id) VALUES(?,?,?) ON CONFLICT(stream_id,agent_id) DO NOTHING`, streamID, scopeID, agentID)
		if err != nil {
			return OutputStream{}, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return OutputStream{}, err
		}
		changed = rows > 0
	} else {
		result, err := tx.ExecContext(ctx, `DELETE FROM output_stream_publishers WHERE stream_id=? AND scope_id=? AND agent_id=?`, streamID, scopeID, agentID)
		if err != nil {
			return OutputStream{}, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return OutputStream{}, err
		}
		changed = rows > 0
	}
	if changed {
		eventType := "output.publisher_removed"
		if allowed {
			eventType = "output.publisher_added"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE output_streams SET updated_at=? WHERE scope_id=? AND stream_id=?`, now, scopeID, streamID); err != nil {
			return OutputStream{}, err
		}
		if err := appendEvent(ctx, tx, scopeID, eventType, streamID, eventAttributes("agentId", agentID), now); err != nil {
			return OutputStream{}, err
		}
	}
	stream, err := outputStreamFromQuery(ctx, tx, scopeID, streamID)
	if err != nil {
		return OutputStream{}, err
	}
	if err := tx.Commit(); err != nil {
		return OutputStream{}, err
	}
	return stream, nil
}

func consumeOutputQuota(ctx context.Context, tx sqlExecutor, scopeID, principalType, principalID string, publish bool, now int64) error {
	window := now - now%60000
	if _, err := tx.ExecContext(ctx, `DELETE FROM output_rate_usage WHERE scope_id=? AND window_start<?`, scopeID, window-60000); err != nil {
		return err
	}
	column, cap := "read_count", outputReadRate
	if publish {
		column, cap = "publish_count", outputPublishRate
	}
	statement := `INSERT INTO output_rate_usage(scope_id,principal_type,principal_id,window_start,` + column + `) VALUES(?,?,?,?,1)
	ON CONFLICT(scope_id,principal_type,principal_id,window_start) DO UPDATE SET ` + column + `=output_rate_usage.` + column + `+1 RETURNING ` + column
	var count int
	if err := tx.QueryRowContext(ctx, statement, scopeID, principalType, principalID, window).Scan(&count); err != nil {
		return err
	}
	if count > cap {
		return Errorf(CodeBackpressure, "Output request rate limit is full")
	}
	return nil
}

type outputPublisher struct {
	Agent      *Principal
	Credential string
}

func (s *Store) PublishOutput(ctx context.Context, authority outputPublisher, streamID string, input PublishOutputInput, valueJSON, referenceJSON string) (OutputValue, string, error) {
	// Resolve the lock key only. Authority and existence are checked again
	// inside the transaction after all competing scope operations finish.
	scopeID, err := s.outputScope(ctx, streamID, authority.Agent == nil)
	if err != nil {
		return OutputValue{}, "", err
	}
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return OutputValue{}, "", err
	}
	defer tx.Rollback()
	record, err := scanOutputStream(tx.QueryRowContext(ctx, `SELECT `+outputStreamColumns+` FROM output_streams WHERE stream_id=?`, streamID))
	if errors.Is(err, sql.ErrNoRows) {
		if authority.Agent == nil {
			return OutputValue{}, "", Errorf(CodeUnauthenticated, "Invalid output credential")
		}
		return OutputValue{}, "", Errorf(CodeNotFound, "Output stream "+streamID+" was not found")
	}
	if err != nil {
		return OutputValue{}, "", err
	}
	producerType, producerID := "", ""
	if authority.Agent != nil {
		principal := *authority.Agent
		if principal.ScopeID != record.ScopeID {
			return OutputValue{}, "", Errorf(CodeNotFound, "Output stream "+streamID+" was not found")
		}
		if err := requireCurrentExecution(ctx, tx, principal, nowMillis()); err != nil {
			return OutputValue{}, "", err
		}
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM output_stream_publishers WHERE stream_id=? AND scope_id=? AND agent_id=?`, streamID, record.ScopeID, principal.AgentID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
			return OutputValue{}, "", Errorf(CodePermissionDenied, "Agent is not allowed to publish to this output stream")
		} else if err != nil {
			return OutputValue{}, "", err
		}
		producerType, producerID = "agent", principal.AgentID
	} else {
		credential, err := authenticateScopedCredential(ctx, tx, authority.Credential, scopedCredentialGrant{
			ResourceType: outputStreamResource, ResourceID: streamID, Permission: string(OutputPublish),
		})
		if err != nil {
			var failure *BusError
			if errors.As(err, &failure) && failure.Code == CodeUnauthenticated {
				return OutputValue{}, "", Errorf(CodeUnauthenticated, "Invalid output credential")
			}
			return OutputValue{}, "", err
		}
		if credential.ScopeID != record.ScopeID {
			return OutputValue{}, "", Errorf(CodeUnauthenticated, "Invalid output credential")
		}
		producerType, producerID = "principal", credential.ID
	}
	if err := requireHostedNewWork(ctx, tx, record.ScopeID); err != nil {
		return OutputValue{}, "", err
	}
	now := nowMillis()
	if err := consumeOutputQuota(ctx, tx, record.ScopeID, producerType, producerID, true, now); err != nil {
		return OutputValue{}, "", err
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `UPDATE output_streams SET sequence=sequence+1,updated_at=? WHERE stream_id=? RETURNING sequence`, now, streamID).Scan(&sequence); err != nil {
		return OutputValue{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO output_values(stream_id,sequence,producer_type,producer_id,content_type,value_json,reference_json,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		streamID, sequence, producerType, producerID, input.ContentType, valueJSON, nullableString(referenceJSON), now); err != nil {
		return OutputValue{}, "", err
	}
	floor := sequence - int64(record.RetentionLimit)
	if floor > record.MinimumCursor {
		if _, err := tx.ExecContext(ctx, `DELETE FROM output_values WHERE stream_id=? AND sequence<=?`, streamID, floor); err != nil {
			return OutputValue{}, "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE output_streams SET floor_sequence=? WHERE stream_id=?`, floor, streamID); err != nil {
			return OutputValue{}, "", err
		}
	}
	if err := appendEvent(ctx, tx, record.ScopeID, "output.published", streamID, eventAttributes(
		"sequence", fmt.Sprintf("%d", sequence), "producerType", producerType, "producerId", producerID, "contentType", string(input.ContentType),
	), now); err != nil {
		return OutputValue{}, "", err
	}
	value := OutputValue{
		StreamID: streamID, Sequence: sequence, ProducerType: producerType, ProducerID: producerID,
		ContentType: input.ContentType, Value: input.Value, Reference: input.Reference, CreatedAt: instant(now),
	}
	if err := tx.Commit(); err != nil {
		return OutputValue{}, "", err
	}
	return value, record.ScopeID, nil
}

func scanOutputValue(row rowScanner) (OutputValue, error) {
	var value OutputValue
	var valueJSON string
	var referenceJSON sql.NullString
	var createdAt int64
	if err := row.Scan(&value.StreamID, &value.Sequence, &value.ProducerType, &value.ProducerID, &value.ContentType, &valueJSON, &referenceJSON, &createdAt); err != nil {
		return OutputValue{}, err
	}
	if err := json.Unmarshal([]byte(valueJSON), &value.Value); err != nil {
		return OutputValue{}, err
	}
	if referenceJSON.Valid {
		value.Reference = &OutputReference{}
		if err := json.Unmarshal([]byte(referenceJSON.String), value.Reference); err != nil {
			return OutputValue{}, err
		}
	}
	value.CreatedAt = instant(createdAt)
	return value, nil
}

const outputValueColumns = `stream_id,sequence,producer_type,producer_id,content_type,value_json,reference_json,created_at`

func outputHistory(ctx context.Context, tx sqlExecutor, stream outputStreamRecord, after int64, limit int) (OutputHistory, error) {
	history := OutputHistory{
		StreamID: stream.ID, Values: []OutputValue{}, NextSequence: after,
		CurrentSequence: stream.CurrentSequence, MinimumCursor: stream.MinimumCursor,
	}
	if after > stream.CurrentSequence {
		return OutputHistory{}, Errorf(CodeInvalidArgument, "after exceeds the current output sequence")
	}
	if after < stream.MinimumCursor {
		history.ResyncRequired = true
		history.NextSequence = stream.CurrentSequence
		return history, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+outputValueColumns+` FROM output_values WHERE stream_id=? AND sequence>? ORDER BY sequence LIMIT ?`, stream.ID, after, limit)
	if err != nil {
		return OutputHistory{}, err
	}
	for rows.Next() {
		value, err := scanOutputValue(rows)
		if err != nil {
			rows.Close()
			return OutputHistory{}, err
		}
		history.Values = append(history.Values, value)
		history.NextSequence = value.Sequence
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return OutputHistory{}, err
	}
	rows.Close()
	return history, nil
}

func (s *Store) readOutput(ctx context.Context, scopeID, credential, streamID string, after int64, limit int, latest bool) (OutputHistory, *OutputValue, error) {
	lockScope, err := s.outputScope(ctx, streamID, credential != "")
	if err != nil {
		return OutputHistory{}, nil, err
	}
	tx, err := s.beginTx(ctx, lockScope)
	if err != nil {
		return OutputHistory{}, nil, err
	}
	defer tx.Rollback()
	stream, err := scanOutputStream(tx.QueryRowContext(ctx, `SELECT `+outputStreamColumns+` FROM output_streams WHERE stream_id=?`, streamID))
	if errors.Is(err, sql.ErrNoRows) {
		if credential != "" {
			return OutputHistory{}, nil, Errorf(CodeUnauthenticated, "Invalid output credential")
		}
		return OutputHistory{}, nil, Errorf(CodeNotFound, "Output stream "+streamID+" was not found")
	}
	if err != nil {
		return OutputHistory{}, nil, err
	}
	if credential == "" {
		if scopeID != stream.ScopeID {
			return OutputHistory{}, nil, Errorf(CodeNotFound, "Output stream "+streamID+" was not found")
		}
	} else {
		principal, err := authenticateScopedCredential(ctx, tx, credential, scopedCredentialGrant{
			ResourceType: outputStreamResource, ResourceID: streamID, Permission: string(OutputRead),
		})
		if err != nil {
			var failure *BusError
			if errors.As(err, &failure) && failure.Code == CodeUnauthenticated {
				return OutputHistory{}, nil, Errorf(CodeUnauthenticated, "Invalid output credential")
			}
			return OutputHistory{}, nil, err
		}
		if principal.ScopeID != stream.ScopeID {
			return OutputHistory{}, nil, Errorf(CodeUnauthenticated, "Invalid output credential")
		}
		if err := consumeOutputQuota(ctx, tx, stream.ScopeID, "principal", principal.ID, false, nowMillis()); err != nil {
			return OutputHistory{}, nil, err
		}
	}
	if latest {
		value, err := scanOutputValue(tx.QueryRowContext(ctx, `SELECT `+outputValueColumns+` FROM output_values WHERE stream_id=? ORDER BY sequence DESC LIMIT 1`, streamID))
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.Commit(); err != nil {
				return OutputHistory{}, nil, err
			}
			return OutputHistory{}, nil, nil
		}
		if err != nil {
			return OutputHistory{}, nil, err
		}
		if err := tx.Commit(); err != nil {
			return OutputHistory{}, nil, err
		}
		return OutputHistory{}, &value, nil
	}
	history, err := outputHistory(ctx, tx, stream, after, limit)
	if err != nil {
		return OutputHistory{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return OutputHistory{}, nil, err
	}
	return history, nil, nil
}

func (s *Store) outputScope(ctx context.Context, streamID string, private bool) (string, error) {
	var scopeID string
	err := s.db.QueryRowContext(ctx, "SELECT scope_id FROM output_streams WHERE stream_id=?", streamID).Scan(&scopeID)
	if errors.Is(err, sql.ErrNoRows) {
		if private {
			return "", Errorf(CodeUnauthenticated, "Invalid output credential")
		}
		return "", Errorf(CodeNotFound, "Output stream "+streamID+" was not found")
	}
	return scopeID, err
}

func normalizedOutputRetention(value int) (int, error) {
	if value == 0 {
		return defaultOutputRetention, nil
	}
	if value < 1 || value > maxOutputRetention {
		return 0, Errorf(CodeInvalidArgument, "retentionLimit must be between 1 and 10000")
	}
	return value, nil
}

func normalizedOutputLimit(value int) (int, error) {
	if value == 0 {
		return defaultOutputLimit, nil
	}
	if value < 1 || value > maxOutputLimit {
		return 0, Errorf(CodeInvalidArgument, "limit must be between 1 and 100")
	}
	return value, nil
}

func validateOutputPublishers(values []string) ([]string, error) {
	if len(values) > maxOutputPublishers {
		return nil, Errorf(CodeInvalidArgument, "publisherAgentIds exceeds 128 items")
	}
	uniqueValues := unique(values)
	if len(uniqueValues) != len(values) {
		return nil, Errorf(CodeInvalidArgument, "publisherAgentIds must be unique")
	}
	values = uniqueValues
	for _, value := range values {
		if err := validateIdentity(value, "publisherAgentIds", false); err != nil {
			return nil, err
		}
	}
	sort.Strings(values)
	return values, nil
}

func validatePublishOutput(input PublishOutputInput) (string, string, error) {
	if input.Value == nil {
		return "", "", Errorf(CodeInvalidArgument, "value is required")
	}
	valueJSON, err := json.Marshal(input.Value)
	if err != nil {
		return "", "", Errorf(CodeInvalidArgument, "value must be valid JSON")
	}
	switch input.ContentType {
	case OutputText:
		text, ok := input.Value.(string)
		if !ok {
			return "", "", Errorf(CodeInvalidArgument, "text/plain value must be a string")
		}
		if len(text) > maxOutputTextBytes {
			return "", "", Errorf(CodeInvalidArgument, "text/plain value exceeds 65536 bytes")
		}
	case OutputJSON:
		if len(valueJSON) > maxOutputJSONBytes {
			return "", "", Errorf(CodeInvalidArgument, "application/json value exceeds 262144 bytes")
		}
	default:
		return "", "", Errorf(CodeInvalidArgument, "contentType is invalid")
	}
	referenceJSON := ""
	if input.Reference != nil {
		if err := validateText(input.Reference.URI, "reference.uri", 4096, false); err != nil {
			return "", "", err
		}
		parsed, err := url.Parse(input.Reference.URI)
		if err != nil || parsed.Scheme == "" {
			return "", "", Errorf(CodeInvalidArgument, "reference.uri must be an absolute URI")
		}
		if err := validateText(input.Reference.Title, "reference.title", 512, true); err != nil {
			return "", "", err
		}
		data, err := json.Marshal(input.Reference)
		if err != nil {
			return "", "", err
		}
		referenceJSON = string(data)
	}
	return string(valueJSON), referenceJSON, nil
}

func (r *Runtime) CreateOutputStream(ctx context.Context, scopeToken string, input CreateOutputStreamInput) (OutputStream, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return OutputStream{}, err
	}
	if err := validateIdentity(input.Name, "name", false); err != nil {
		return OutputStream{}, err
	}
	input.RetentionLimit, err = normalizedOutputRetention(input.RetentionLimit)
	if err != nil {
		return OutputStream{}, err
	}
	input.PublisherAgentIDs, err = validateOutputPublishers(input.PublisherAgentIDs)
	if err != nil {
		return OutputStream{}, err
	}
	stream, err := r.store.CreateOutputStream(ctx, scopeID, input)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return stream, err
}

func (r *Runtime) ListOutputStreams(ctx context.Context, scopeToken string) ([]OutputStream, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return nil, err
	}
	return r.store.ListOutputStreams(ctx, scopeID)
}

func (r *Runtime) OutputStream(ctx context.Context, scopeToken, streamID string) (OutputStream, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return OutputStream{}, err
	}
	if err := validateIdentity(streamID, "streamId", false); err != nil {
		return OutputStream{}, err
	}
	return r.store.OutputStream(ctx, scopeID, streamID)
}

func (r *Runtime) RemoveOutputStream(ctx context.Context, scopeToken, streamID string) error {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return err
	}
	if err := validateIdentity(streamID, "streamId", false); err != nil {
		return err
	}
	if err := r.store.RemoveOutputStream(ctx, scopeID, streamID); err != nil {
		return err
	}
	r.notifyScope(scopeID)
	return nil
}

func (r *Runtime) SetOutputPublisher(ctx context.Context, scopeToken, streamID, agentID string, allowed bool) (OutputStream, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return OutputStream{}, err
	}
	if err := validateIdentity(streamID, "streamId", false); err != nil {
		return OutputStream{}, err
	}
	if err := validateIdentity(agentID, "agentId", false); err != nil {
		return OutputStream{}, err
	}
	stream, err := r.store.SetOutputPublisher(ctx, scopeID, streamID, agentID, allowed)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return stream, err
}

func (r *Runtime) PublishOutput(ctx context.Context, token, streamID string, input PublishOutputInput) (OutputValue, error) {
	if err := validateIdentity(streamID, "streamId", false); err != nil {
		return OutputValue{}, err
	}
	valueJSON, referenceJSON, err := validatePublishOutput(input)
	if err != nil {
		return OutputValue{}, err
	}
	authority := outputPublisher{Credential: token}
	if principal, err := r.store.AuthenticateAgent(ctx, token); err == nil {
		authority = outputPublisher{Agent: &principal}
	}
	value, scopeID, err := r.store.PublishOutput(ctx, authority, streamID, input, valueJSON, referenceJSON)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return value, err
}

func (r *Runtime) outputReadAuthority(ctx context.Context, token string) (string, string) {
	if scopeID, err := r.store.AuthenticateScope(ctx, token); err == nil {
		return scopeID, ""
	}
	return "", token
}

func (r *Runtime) LatestOutput(ctx context.Context, token, streamID string) (*OutputValue, error) {
	if err := validateIdentity(streamID, "streamId", false); err != nil {
		return nil, err
	}
	if token == "" {
		return nil, Errorf(CodeUnauthenticated, "Output credential is required")
	}
	scopeID, credential := r.outputReadAuthority(ctx, token)
	if scopeID != "" {
		ctx = withScopeCredential(ctx, token)
	}
	_, value, err := r.store.readOutput(ctx, scopeID, credential, streamID, 0, 0, true)
	return value, err
}

func (r *Runtime) OutputHistory(ctx context.Context, token, streamID string, after int64, limit int) (OutputHistory, error) {
	if err := validateIdentity(streamID, "streamId", false); err != nil {
		return OutputHistory{}, err
	}
	if after < 0 {
		return OutputHistory{}, Errorf(CodeInvalidArgument, "after must not be negative")
	}
	if token == "" {
		return OutputHistory{}, Errorf(CodeUnauthenticated, "Output credential is required")
	}
	limit, err := normalizedOutputLimit(limit)
	if err != nil {
		return OutputHistory{}, err
	}
	scopeID, credential := r.outputReadAuthority(ctx, token)
	if scopeID != "" {
		ctx = withScopeCredential(ctx, token)
	}
	history, _, err := r.store.readOutput(ctx, scopeID, credential, streamID, after, limit, false)
	return history, err
}
