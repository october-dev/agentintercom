package bus

import (
	"context"
	"strings"
)

type ArchivedChannel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"createdAt"`
}
type ArchivedChannelSubscription struct {
	ChannelID string `json:"channelId"`
	AgentID   string `json:"agentId"`
	CreatedAt string `json:"createdAt"`
}
type ArchivedChannelPost struct {
	ChannelPost
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

func exportChannels(ctx context.Context, tx sqlExecutor, scopeID string, archive *ScopeArchive) error {
	archive.Channels = []ArchivedChannel{}
	archive.ChannelSubscriptions = []ArchivedChannelSubscription{}
	archive.ChannelPosts = []ArchivedChannelPost{}
	rows, err := tx.QueryContext(ctx, "SELECT channel_id,name,description,created_at FROM channels WHERE scope_id=? ORDER BY channel_id", scopeID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c ArchivedChannel
		var created int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &created); err != nil {
			rows.Close()
			return err
		}
		c.CreatedAt = instant(created)
		archive.Channels = append(archive.Channels, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, "SELECT channel_id,agent_id,created_at FROM channel_subscriptions WHERE scope_id=? ORDER BY channel_id,agent_id", scopeID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c ArchivedChannelSubscription
		var created int64
		if err := rows.Scan(&c.ChannelID, &c.AgentID, &created); err != nil {
			rows.Close()
			return err
		}
		c.CreatedAt = instant(created)
		archive.ChannelSubscriptions = append(archive.ChannelSubscriptions, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, "SELECT post_id,channel_id,author_kind,author_id,body,COALESCE(thread_id,''),COALESCE(idempotency_key,''),created_at FROM channel_posts WHERE scope_id=? ORDER BY created_at,post_id", scopeID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p ArchivedChannelPost
		var created int64
		if err := rows.Scan(&p.ID, &p.ChannelID, &p.AuthorKind, &p.AuthorID, &p.Body, &p.ThreadID, &p.IdempotencyKey, &created); err != nil {
			rows.Close()
			return err
		}
		p.CreatedAt = instant(created)
		archive.ChannelPosts = append(archive.ChannelPosts, p)
	}
	err = rows.Err()
	rows.Close()
	return err
}
func validateArchivedChannels(archive *ScopeArchive, agents map[string]bool) error {
	invalid := func() error {
		return Errorf(CodeInvalidArgument, "Archive contains invalid channels, subscriptions or thread relationships")
	}
	if archive.Version == 2 && (len(archive.Channels) > 0 || len(archive.ChannelPosts) > 0 || len(archive.ChannelSubscriptions) > 0) {
		return invalid()
	}
	if len(archive.Channels) > 100 || len(archive.ChannelSubscriptions) > 12800 || len(archive.ChannelPosts) > 65536 {
		return invalid()
	}
	channels := map[string]bool{}
	names := map[string]bool{}
	for _, c := range archive.Channels {
		if validateIdentity(c.ID, "channel.id", false) != nil || validateCreateChannel(CreateChannelInput{Name: c.Name, Description: c.Description}) != nil || channels[c.ID] || names[c.Name] {
			return invalid()
		}
		if _, err := archiveTime(c.CreatedAt, true); err != nil {
			return err
		}
		channels[c.ID] = true
		names[c.Name] = true
	}
	subscriptions := map[string]bool{}
	counts := map[string]int{}
	for _, s := range archive.ChannelSubscriptions {
		key := s.ChannelID + "\x00" + s.AgentID
		counts[s.ChannelID]++
		if !channels[s.ChannelID] || !agents[s.AgentID] || subscriptions[key] || counts[s.ChannelID] > 128 {
			return invalid()
		}
		subscriptions[key] = true
		if _, err := archiveTime(s.CreatedAt, true); err != nil {
			return err
		}
	}
	posts := map[string]ArchivedChannelPost{}
	keys := map[string]bool{}
	for _, p := range archive.ChannelPosts {
		if !channels[p.ChannelID] || posts[p.ID].ID != "" || validateIdentity(p.ID, "post.id", false) != nil || validateIdentity(p.AuthorID, "authorId", false) != nil || validatePostChannel(PostChannelInput{ChannelID: p.ChannelID, Body: p.Body, ThreadID: p.ThreadID, IdempotencyKey: p.IdempotencyKey}) != nil {
			return invalid()
		}
		if p.AuthorKind != MessageParticipantHuman && (p.AuthorKind != MessageParticipantAgent || !agents[p.AuthorID]) {
			return invalid()
		}
		if _, err := archiveTime(p.CreatedAt, true); err != nil {
			return err
		}
		if p.IdempotencyKey != "" {
			key := string(p.AuthorKind) + "\x00" + p.AuthorID + "\x00" + p.IdempotencyKey
			if keys[key] {
				return invalid()
			}
			keys[key] = true
		}
		posts[p.ID] = p
	}
	for _, p := range archive.ChannelPosts {
		if p.ThreadID != "" {
			root, ok := posts[p.ThreadID]
			if !ok || root.ChannelID != p.ChannelID || root.ThreadID != "" || root.ID == p.ID {
				return invalid()
			}
		}
	}
	return nil
}
func importChannels(ctx context.Context, tx sqlExecutor, archive ScopeArchive) error {
	for _, c := range archive.Channels {
		created, _ := archiveTime(c.CreatedAt, true)
		if _, err := tx.ExecContext(ctx, "INSERT INTO channels(scope_id,channel_id,name,description,created_at) VALUES(?,?,?,?,?)", archive.Scope.ID, c.ID, c.Name, c.Description, created); err != nil {
			return err
		}
	}
	for _, s := range archive.ChannelSubscriptions {
		created, _ := archiveTime(s.CreatedAt, true)
		if _, err := tx.ExecContext(ctx, "INSERT INTO channel_subscriptions(scope_id,channel_id,agent_id,created_at) VALUES(?,?,?,?)", archive.Scope.ID, s.ChannelID, s.AgentID, created); err != nil {
			return err
		}
	}
	// Roots precede replies regardless of archive ordering or millisecond ties.
	for _, roots := range []bool{true, false} {
		for _, p := range archive.ChannelPosts {
			if (p.ThreadID == "") != roots {
				continue
			}
			created, _ := archiveTime(p.CreatedAt, true)
			hash := channelPostHash(PostChannelInput{ChannelID: p.ChannelID, Body: p.Body, ThreadID: p.ThreadID})
			if _, err := tx.ExecContext(ctx, "INSERT INTO channel_posts(scope_id,post_id,channel_id,author_kind,author_id,body,search_body,thread_id,idempotency_key,request_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)", archive.Scope.ID, p.ID, p.ChannelID, p.AuthorKind, p.AuthorID, p.Body, strings.ToLower(p.Body), nullableString(p.ThreadID), nullableString(p.IdempotencyKey), hash, created); err != nil {
				return err
			}
		}
	}
	return nil
}

// Whole threads are retained together. The minimum seven-day lifetime also
// covers the bounded lifetime of their durable inbox notifications.
func channelRetentionCandidates(ctx context.Context, tx sqlExecutor, scopeID string, before int64) ([]string, int64, error) {
	cutoff := min(before, nowMillis()-int64(7*24*60*60*1000))
	rows, err := tx.QueryContext(ctx, "SELECT COALESCE(thread_id,post_id),COUNT(*) FROM channel_posts WHERE scope_id=? GROUP BY COALESCE(thread_id,post_id) HAVING MAX(created_at)<?", scopeID, cutoff)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	roots := []string{}
	var count int64
	for rows.Next() {
		var root string
		var n int64
		if err := rows.Scan(&root, &n); err != nil {
			return nil, 0, err
		}
		roots = append(roots, root)
		count += n
	}
	return roots, count, rows.Err()
}
