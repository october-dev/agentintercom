package bus

import (
	"context"
	"database/sql"
	"errors"
)

type agentCardPublication struct {
	ID        string
	ScopeID   string
	AgentID   string
	Enabled   bool
	CreatedAt int64
	UpdatedAt int64
}

func scanAgentCardPublication(row rowScanner) (agentCardPublication, error) {
	var publication agentCardPublication
	var enabled int
	err := row.Scan(&publication.ID, &publication.ScopeID, &publication.AgentID, &enabled, &publication.CreatedAt, &publication.UpdatedAt)
	publication.Enabled = enabled == 1
	return publication, err
}

const agentCardPublicationColumns = `publication_id,scope_id,agent_id,enabled,created_at,updated_at`

func (s *Store) CreateAgentCardPublication(ctx context.Context, scopeID, agentID string) (agentCardPublication, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return agentCardPublication{}, err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE scope_id=? AND agent_id=?`, scopeID, agentID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return agentCardPublication{}, Errorf(CodeNotFound, "Agent "+agentID+" was not found")
	} else if err != nil {
		return agentCardPublication{}, err
	}
	publicationID, err := randomID("pub_")
	if err != nil {
		return agentCardPublication{}, err
	}
	now := nowMillis()
	_, err = tx.ExecContext(ctx, `INSERT INTO a2a_publications(publication_id,scope_id,agent_id,enabled,created_at,updated_at) VALUES(?,?,?,1,?,?)`, publicationID, scopeID, agentID, now, now)
	if err != nil {
		if isStorageConstraint(err) {
			return agentCardPublication{}, Errorf(CodeConflict, "Agent "+agentID+" already has a publication")
		}
		return agentCardPublication{}, err
	}
	if err := appendEvent(ctx, tx, scopeID, "a2a.publication_created", publicationID, eventAttributes("agentId", agentID, "enabled", "true"), now); err != nil {
		return agentCardPublication{}, err
	}
	publication, err := scanAgentCardPublication(tx.QueryRowContext(ctx, `SELECT `+agentCardPublicationColumns+` FROM a2a_publications WHERE publication_id=?`, publicationID))
	if err != nil {
		return agentCardPublication{}, err
	}
	if err := tx.Commit(); err != nil {
		return agentCardPublication{}, err
	}
	return publication, nil
}

func (s *Store) ListAgentCardPublications(ctx context.Context, scopeID string) ([]agentCardPublication, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentCardPublicationColumns+` FROM a2a_publications WHERE scope_id=? ORDER BY created_at,publication_id`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	publications := []agentCardPublication{}
	for rows.Next() {
		publication, err := scanAgentCardPublication(rows)
		if err != nil {
			return nil, err
		}
		publications = append(publications, publication)
	}
	return publications, rows.Err()
}

func (s *Store) SetAgentCardPublicationEnabled(ctx context.Context, scopeID, publicationID string, enabled bool) (agentCardPublication, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return agentCardPublication{}, err
	}
	defer tx.Rollback()
	publication, err := scanAgentCardPublication(tx.QueryRowContext(ctx, `SELECT `+agentCardPublicationColumns+` FROM a2a_publications WHERE scope_id=? AND publication_id=?`, scopeID, publicationID))
	if errors.Is(err, sql.ErrNoRows) {
		return agentCardPublication{}, Errorf(CodeNotFound, "Agent Card publication was not found")
	}
	if err != nil {
		return agentCardPublication{}, err
	}
	if publication.Enabled == enabled {
		if err := tx.Commit(); err != nil {
			return agentCardPublication{}, err
		}
		return publication, nil
	}
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, `UPDATE a2a_publications SET enabled=?,updated_at=? WHERE scope_id=? AND publication_id=?`, enabled, now, scopeID, publicationID); err != nil {
		return agentCardPublication{}, err
	}
	eventType := "a2a.publication_disabled"
	if enabled {
		eventType = "a2a.publication_enabled"
	}
	if err := appendEvent(ctx, tx, scopeID, eventType, publicationID, eventAttributes("agentId", publication.AgentID, "enabled", boolString(enabled)), now); err != nil {
		return agentCardPublication{}, err
	}
	publication.Enabled = enabled
	publication.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return agentCardPublication{}, err
	}
	return publication, nil
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func (s *Store) PublishedAgent(ctx context.Context, publicationID string) (agentCardPublication, Agent, error) {
	publication, err := scanAgentCardPublication(s.db.QueryRowContext(ctx, `SELECT `+agentCardPublicationColumns+` FROM a2a_publications WHERE publication_id=? AND enabled=1`, publicationID))
	if errors.Is(err, sql.ErrNoRows) {
		return agentCardPublication{}, Agent{}, Errorf(CodeNotFound, "Agent Card was not found")
	}
	if err != nil {
		return agentCardPublication{}, Agent{}, err
	}
	agent, err := s.Agent(ctx, publication.ScopeID, publication.AgentID)
	if err != nil {
		return agentCardPublication{}, Agent{}, err
	}
	return publication, agent, nil
}

func (r *Runtime) CreateAgentCardPublication(ctx context.Context, scopeToken string, input PublishAgentCardInput) (agentCardPublication, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return agentCardPublication{}, err
	}
	if err := validateIdentity(input.AgentID, "agentId", false); err != nil {
		return agentCardPublication{}, err
	}
	publication, err := r.store.CreateAgentCardPublication(ctx, scopeID, input.AgentID)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return publication, err
}

func (r *Runtime) ListAgentCardPublications(ctx context.Context, scopeToken string) ([]agentCardPublication, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return nil, err
	}
	return r.store.ListAgentCardPublications(ctx, scopeID)
}

func (r *Runtime) SetAgentCardPublicationEnabled(ctx context.Context, scopeToken, publicationID string, enabled bool) (agentCardPublication, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return agentCardPublication{}, err
	}
	if err := validateIdentity(publicationID, "publicationId", false); err != nil {
		return agentCardPublication{}, err
	}
	publication, err := r.store.SetAgentCardPublicationEnabled(ctx, scopeID, publicationID, enabled)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return publication, err
}
