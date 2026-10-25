package bus

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/october-dev/october-bus/deploy/hosted/migrations"
)

const MaxAttachmentBytes = 16 * 1024 * 1024
const MaxAttachmentChunkBytes = 512 * 1024
const attachmentStorageBytes int64 = 5 * 1024 * 1024 * 1024
const attachmentTransferBytes int64 = 25 * 1024 * 1024 * 1024

var attachmentIDPattern = regexp.MustCompile(`^att_[0-9a-f]{32}$`)
var attachmentsSchemaSQL = func() string {
	data, err := migrations.Files.ReadFile("009_attachments.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}()

type Attachment struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	MediaType  string                 `json:"mediaType"`
	Size       int64                  `json:"size"`
	SHA256     string                 `json:"sha256"`
	AuthorKind MessageParticipantKind `json:"authorKind"`
	AuthorID   string                 `json:"authorId"`
	CreatedAt  string                 `json:"createdAt"`
	URI        string                 `json:"uri"`
}
type UploadAttachmentInput struct {
	Name           string `json:"name"`
	MediaType      string `json:"mediaType,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}
type AttachmentPage struct {
	Attachments []Attachment `json:"attachments"`
	NextCursor  string       `json:"nextCursor,omitempty"`
}
type AttachmentContent struct {
	Attachment Attachment `json:"attachment"`
	Offset     int64      `json:"offset"`
	Data       []byte     `json:"dataBase64"`
	NextOffset *int64     `json:"nextOffset,omitempty"`
}

// A single service owns the private file directory. A directory lock rejects
// multiple writers; PostgreSQL alone does not replicate attachment bytes.
type attachmentFiles struct {
	minimumFreeBytes uint64
	maintenance      sync.RWMutex
	mu               sync.Mutex
	directory        string
	root             *os.Root
	lock             *os.File
	temporary        bool
}

func (f *attachmentFiles) open(source string) error {
	if f.root != nil {
		return nil
	}
	if f.directory == "" {
		if source == "" {
			return Errorf(CodeInternal, "Attachment storage is not configured")
		}
		if source == ":memory:" {
			dir, err := os.MkdirTemp("", "october-bus-files-")
			if err != nil {
				return err
			}
			f.directory, f.temporary = dir, true
		} else {
			f.directory = source + ".attachments"
		}
	}
	if err := os.MkdirAll(f.directory, 0700); err != nil {
		return err
	}
	if err := checkPrivatePath(f.directory, true); err != nil {
		return err
	}
	root, err := os.OpenRoot(f.directory)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(f.directory, ".lock")
	if _, err := os.Lstat(lockPath); err == nil {
		if err := checkPrivatePath(lockPath, false); err != nil {
			root.Close()
			return err
		}
	} else if !os.IsNotExist(err) {
		root.Close()
		return err
	}
	lock, err := acquireFileLock(lockPath)
	if err != nil {
		root.Close()
		return Errorf(CodeConflict, "Attachment directory is already owned by another service")
	}
	f.root, f.lock = root, lock
	return nil
}
func (f *attachmentFiles) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.root != nil {
		_ = f.root.Close()
	}
	if f.lock != nil {
		_ = f.lock.Close()
	}
	if f.temporary {
		_ = os.RemoveAll(f.directory)
	}
}
func (s *Store) configureAttachments(directory string) error {
	if directory == "" {
		return nil
	}
	s.files.directory = directory
	return s.files.open(s.source)
}
func validateAttachmentInput(input *UploadAttachmentInput) error {
	if err := validateText(input.Name, "name", 255, false); err != nil {
		return err
	}
	if input.Name == "." || input.Name == ".." || strings.ContainsAny(input.Name, "/\\") || strings.IndexFunc(input.Name, unicode.IsControl) >= 0 {
		return Errorf(CodeInvalidArgument, "Attachment name must be a filename without paths or control characters")
	}
	if input.MediaType == "" {
		input.MediaType = "application/octet-stream"
	}
	if len(input.MediaType) > 128 {
		return Errorf(CodeInvalidArgument, "Invalid attachment media type")
	}
	media, params, err := mime.ParseMediaType(input.MediaType)
	if err != nil || len(params) != 0 || !strings.Contains(media, "/") {
		return Errorf(CodeInvalidArgument, "Attachment media type must be a type/subtype without parameters")
	}
	input.MediaType = media
	return validateIdentity(input.IdempotencyKey, "idempotencyKey", true)
}

const attachmentColumns = "attachment_id,name,media_type,size_bytes,sha256,author_kind,author_id,created_at"

func scanAttachment(row rowScanner) (Attachment, error) {
	var a Attachment
	var created int64
	err := row.Scan(&a.ID, &a.Name, &a.MediaType, &a.Size, &a.SHA256, &a.AuthorKind, &a.AuthorID, &created)
	a.CreatedAt = instant(created)
	a.URI = "october-bus://attachments/" + a.ID
	return a, err
}
func attachmentFrom(ctx context.Context, q sqlExecutor, scopeID, id string) (Attachment, error) {
	if !attachmentIDPattern.MatchString(id) {
		return Attachment{}, Errorf(CodeInvalidArgument, "Invalid attachment ID")
	}
	a, err := scanAttachment(q.QueryRowContext(ctx, "SELECT "+attachmentColumns+" FROM attachments WHERE scope_id=? AND attachment_id=? AND deleted_at=0", scopeID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, Errorf(CodeNotFound, "Attachment was not found")
	}
	return a, err
}
func attachmentLimits(ctx context.Context, q sqlExecutor, scopeID string) (int64, int64, error) {
	storage, transfer := attachmentStorageBytes, attachmentTransferBytes
	if isPostgresQuery(q) {
		var paid int64
		err := q.QueryRowContext(ctx, "SELECT paid_until FROM hosted_workspaces WHERE scope_id=?", scopeID).Scan(&paid)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, 0, err
		}
		if err == nil && paid <= nowMillis() {
			storage, transfer = 256*1024*1024, 1024*1024*1024
		}
	}
	return storage, transfer, nil
}
func consumeAttachmentTransfer(ctx context.Context, tx sqlExecutor, scopeID string, size int64, upload bool) error {
	_, limit, err := attachmentLimits(ctx, tx, scopeID)
	if err != nil {
		return err
	}
	month := time.Now().UTC().Format("2006-01")
	column := "downloaded_bytes"
	if upload {
		column = "uploaded_bytes"
	}
	var used int64
	err = tx.QueryRowContext(ctx, "SELECT "+column+" FROM attachment_usage WHERE scope_id=? AND month=?", scopeID, month).Scan(&used)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if size > limit-used {
		return Errorf(CodeBackpressure, "Workspace monthly attachment transfer allowance is full")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO attachment_usage(scope_id,month,"+column+") VALUES(?,?,?) ON CONFLICT(scope_id,month) DO UPDATE SET "+column+"=attachment_usage."+column+"+?", scopeID, month, size, size)
	return err
}
func attachmentRequestHash(input UploadAttachmentInput, digest string) string {
	input.IdempotencyKey = ""
	raw, _ := json.Marshal(struct {
		Input UploadAttachmentInput
		SHA   string
	}{input, digest})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func (s *Store) UploadAttachment(ctx context.Context, scopeID string, input UploadAttachmentInput, data []byte) (Attachment, error) {
	s.files.maintenance.RLock()
	defer s.files.maintenance.RUnlock()
	var zero Attachment
	if len(data) > MaxAttachmentBytes {
		return zero, Errorf(CodeInvalidArgument, "Attachment exceeds 16 MiB")
	}
	if err := validateAttachmentInput(&input); err != nil {
		return zero, err
	}
	kind, actor, err := channelAuthor(ctx, scopeID)
	if err != nil {
		return zero, err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	requestHash := attachmentRequestHash(input, digest)
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if input.IdempotencyKey != "" {
		var id, previous string
		var deleted int64
		err = tx.QueryRowContext(ctx, "SELECT attachment_id,request_hash,deleted_at FROM attachments WHERE scope_id=? AND author_kind=? AND author_id=? AND idempotency_key=?", scopeID, kind, actor, input.IdempotencyKey).Scan(&id, &previous, &deleted)
		if err == nil {
			if previous != requestHash || deleted != 0 {
				return zero, Errorf(CodeConflict, "Attachment retry differs from the original upload or the file was deleted")
			}
			a, err := attachmentFrom(ctx, tx, scopeID, id)
			if err != nil {
				return zero, err
			}
			return a, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return zero, err
		}
	}
	if err := requireHostedNewWork(ctx, tx, scopeID); err != nil {
		return zero, err
	}
	limit, _, err := attachmentLimits(ctx, tx, scopeID)
	if err != nil {
		return zero, err
	}
	var used, count int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(size_bytes),0),COUNT(*) FROM attachments WHERE scope_id=? AND deleted_at=0", scopeID).Scan(&used, &count); err != nil {
		return zero, err
	}
	if int64(len(data)) > limit-used || count >= 10000 {
		return zero, Errorf(CodeBackpressure, "Workspace attachment storage allowance is full; delete unneeded files")
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM attachments WHERE scope_id=?", scopeID).Scan(&count); err != nil {
		return zero, err
	}
	if count >= 50000 {
		return zero, Errorf(CodeBackpressure, "Workspace upload record limit is full; deleted upload keys are retained for 30 days")
	}
	if err := consumeAttachmentTransfer(ctx, tx, scopeID, int64(len(data)), true); err != nil {
		return zero, err
	}
	id, err := randomID("att_")
	if err != nil {
		return zero, err
	}
	now := nowMillis()
	s.files.mu.Lock()
	defer s.files.mu.Unlock()
	if err := s.files.open(s.source); err != nil {
		return zero, err
	}
	if err := s.writeAttachmentFile(id, data); err != nil {
		return zero, err
	}
	// The file is durable before metadata commits. An interrupted/ambiguous commit
	// may leave an orphan, never a committed record pointing to a partial file.
	_, err = tx.ExecContext(ctx, "INSERT INTO attachments(attachment_id,scope_id,author_kind,author_id,name,media_type,size_bytes,sha256,idempotency_key,request_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)", id, scopeID, kind, actor, input.Name, input.MediaType, len(data), digest, nullableString(input.IdempotencyKey), requestHash, now)
	if err != nil {
		return zero, err
	}
	if err := appendEvent(ctx, tx, scopeID, "attachment.created", id, eventAttributes("name", input.Name, "size", strconv.Itoa(len(data))), now); err != nil {
		return zero, err
	}
	a := Attachment{ID: id, Name: input.Name, MediaType: input.MediaType, Size: int64(len(data)), SHA256: digest, AuthorKind: kind, AuthorID: actor, CreatedAt: instant(now), URI: "october-bus://attachments/" + id}
	return a, tx.Commit()
}

// Caller owns files.mu and uses a validated/generated ID, never a user path.
func (s *Store) writeAttachmentFile(id string, data []byte) error {
	reserve := s.files.minimumFreeBytes
	if reserve == 0 {
		reserve = 256 * 1024 * 1024
		if s.Backend() == StorageBackendPostgres {
			reserve = 1024 * 1024 * 1024
		}
	}
	available, err := attachmentAvailableSpace(s.files.root, s.files.directory)
	if err != nil {
		return Errorf(CodeInternal, "File storage capacity could not be checked")
	}
	if available < reserve || uint64(len(data)) > available-reserve {
		return Errorf(CodeBackpressure, "File storage is low on disk space; retry after the operator restores capacity")
	}
	file, err := s.files.root.OpenFile(id, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		_ = s.files.root.Remove(id)
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	// Persist the new directory entry before committing the metadata.
	dir, err := s.files.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return syncAttachmentDirectory(dir)
}
func (s *Store) Attachments(ctx context.Context, scopeID, after string, limit int) (AttachmentPage, error) {
	result := AttachmentPage{Attachments: []Attachment{}}
	if _, _, err := channelAuthor(ctx, scopeID); err != nil {
		return result, err
	}
	if after != "" && !attachmentIDPattern.MatchString(after) {
		return result, Errorf(CodeInvalidArgument, "Invalid attachment cursor")
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return result, Errorf(CodeInvalidArgument, "limit must be 1–100")
	}
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+attachmentColumns+" FROM attachments WHERE scope_id=? AND deleted_at=0 AND attachment_id>? ORDER BY attachment_id LIMIT ?", scopeID, after, limit+1)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			rows.Close()
			return result, err
		}
		result.Attachments = append(result.Attachments, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(result.Attachments) > limit {
		result.NextCursor = result.Attachments[limit-1].ID
		result.Attachments = result.Attachments[:limit]
	}
	return result, tx.Commit()
}
func (s *Store) ReadAttachment(ctx context.Context, scopeID, id string, offset int64, limit int) (AttachmentContent, error) {
	s.files.maintenance.RLock()
	defer s.files.maintenance.RUnlock()
	var result AttachmentContent
	if _, _, err := channelAuthor(ctx, scopeID); err != nil {
		return result, err
	}
	if offset < 0 || limit < 1 || limit > MaxAttachmentBytes {
		return result, Errorf(CodeInvalidArgument, "Invalid attachment byte range")
	}
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	a, err := attachmentFrom(ctx, tx, scopeID, id)
	if err != nil {
		return result, err
	}
	if offset > a.Size {
		return result, Errorf(CodeInvalidArgument, "Offset exceeds attachment size")
	}
	size := min(int64(limit), a.Size-offset)
	if err := consumeAttachmentTransfer(ctx, tx, scopeID, size, false); err != nil {
		return result, err
	}
	s.files.mu.Lock()
	defer s.files.mu.Unlock()
	if err := s.files.open(s.source); err != nil {
		return result, err
	}
	data, err := s.readAttachmentFile(a, offset, size)
	if err != nil {
		return result, err
	}
	result = AttachmentContent{Attachment: a, Offset: offset, Data: data}
	if next := offset + size; next < a.Size {
		result.NextOffset = &next
	}
	return result, tx.Commit()
}
func (s *Store) readAttachmentFile(a Attachment, offset, size int64) ([]byte, error) {
	info, err := s.files.root.Lstat(a.ID)
	if err != nil || !info.Mode().IsRegular() || info.Size() != a.Size || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, Errorf(CodeInternal, "Attachment file is unavailable; restore it from backup")
	}
	file, err := s.files.root.Open(a.ID)
	if err != nil {
		return nil, Errorf(CodeInternal, "Attachment file is unavailable")
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil || !os.SameFile(info, current) {
		return nil, Errorf(CodeInternal, "Attachment changed while opening")
	}
	data := make([]byte, size)
	if _, err := file.ReadAt(data, offset); err != nil && !(err == io.EOF && size == 0) {
		return nil, err
	}
	if offset == 0 && size == a.Size {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != a.SHA256 {
			return nil, Errorf(CodeInternal, "Attachment checksum verification failed")
		}
	}
	return data, nil
}
func (s *Store) DeleteAttachment(ctx context.Context, scopeID, id string) error {
	s.files.maintenance.Lock()
	defer s.files.maintenance.Unlock()
	kind, actor, err := channelAuthor(ctx, scopeID)
	if err != nil {
		return err
	}
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := attachmentFrom(ctx, tx, scopeID, id)
	if AsBusError(err).Code == CodeNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if kind != a.AuthorKind || actor != a.AuthorID {
		if kind != MessageParticipantHuman {
			return Errorf(CodePermissionDenied, "Only the uploader or workspace admin can delete this file")
		}
		role, err := hostedMember(ctx, tx, scopeID, actor)
		if err != nil {
			return err
		}
		if role != "owner" && role != "admin" {
			return Errorf(CodePermissionDenied, "Only the uploader or workspace admin can delete this file")
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE attachments SET deleted_at=? WHERE scope_id=? AND attachment_id=?", nowMillis(), scopeID, id); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, scopeID, "attachment.deleted", id, nil, nowMillis()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// The exclusive maintenance lock lets an in-progress backup finish first.
	// If unlinking fails, metadata still blocks reads and maintenance retries it.
	s.files.mu.Lock()
	defer s.files.mu.Unlock()
	if err := s.files.open(s.source); err == nil {
		_ = s.files.root.Remove(id)
	}
	return nil
}

// CollectAttachmentGarbage removes deleted files and old uncommitted uploads.
// A bounded query per directory batch avoids a remote database call per file.
func (s *Store) CollectAttachmentGarbage(ctx context.Context) (int, error) {
	s.files.maintenance.Lock()
	defer s.files.maintenance.Unlock()
	s.files.mu.Lock()
	defer s.files.mu.Unlock()
	if s.files.root == nil {
		directory := s.files.directory
		if directory == "" && s.source != "" && s.source != ":memory:" {
			directory = s.source + ".attachments"
		}
		if directory == "" {
			return 0, nil
		}
		if _, err := os.Stat(directory); os.IsNotExist(err) {
			return 0, nil
		}
		if err := s.files.open(s.source); err != nil {
			return 0, err
		}
	}
	dir, err := s.files.root.Open(".")
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	removed := 0
	for {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		entries, readErr := dir.ReadDir(256)
		if readErr != nil && readErr != io.EOF {
			return removed, readErr
		}
		args := []any{}
		marks := []string{}
		candidates := map[string]os.DirEntry{}
		for _, entry := range entries {
			if attachmentIDPattern.MatchString(entry.Name()) {
				args = append(args, entry.Name())
				marks = append(marks, "?")
				candidates[entry.Name()] = entry
			}
		}
		if len(args) > 0 {
			rows, err := s.db.QueryContext(ctx, "SELECT attachment_id,deleted_at FROM attachments WHERE attachment_id IN ("+strings.Join(marks, ",")+")", args...)
			if err != nil {
				return removed, err
			}
			known := map[string]int64{}
			for rows.Next() {
				var id string
				var deleted int64
				if err := rows.Scan(&id, &deleted); err != nil {
					rows.Close()
					return removed, err
				}
				known[id] = deleted
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return removed, err
			}
			for id, entry := range candidates {
				deleted, exists := known[id]
				if exists && deleted == 0 {
					continue
				}
				if !exists {
					info, err := entry.Info()
					if err != nil {
						return removed, err
					}
					if time.Since(info.ModTime()) < time.Hour {
						continue
					}
				}
				if err := s.files.root.Remove(id); err != nil && !os.IsNotExist(err) {
					return removed, err
				}
				removed++
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM attachments WHERE deleted_at<>0 AND deleted_at<?", nowMillis()-int64(30*24*time.Hour/time.Millisecond)); err != nil {
		return removed, err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM attachment_usage WHERE month<?", time.Now().UTC().AddDate(0, -13, 0).Format("2006-01")); err != nil {
		return removed, err
	}
	return removed, nil
}
