package bus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
)

type ArchivedAttachment struct {
	Attachment
	Data           []byte `json:"dataBase64"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	RequestHash    string `json:"requestHash"`
}

func (s *Store) exportAttachments(ctx context.Context, tx sqlExecutor, scopeID string, archive *ScopeArchive) error {
	rows, err := tx.QueryContext(ctx, "SELECT "+attachmentColumns+",COALESCE(idempotency_key,''),request_hash FROM attachments WHERE scope_id=? AND deleted_at=0 ORDER BY attachment_id", scopeID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a ArchivedAttachment
		a.Attachment, err = scanAttachment(attachmentArchiveRow{rows, &a})
		if err != nil {
			rows.Close()
			return err
		}
		archive.Attachments = append(archive.Attachments, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(archive.Attachments) == 0 {
		return nil
	}
	s.files.mu.Lock()
	defer s.files.mu.Unlock()
	if err := s.files.open(s.source); err != nil {
		return err
	}
	for i := range archive.Attachments {
		a := &archive.Attachments[i]
		data, err := s.readAttachmentFile(a.Attachment, 0, a.Size)
		if err != nil {
			return err
		}
		a.Data = data
	}
	return nil
}

type attachmentArchiveRow struct {
	row rowScanner
	a   *ArchivedAttachment
}

func (r attachmentArchiveRow) Scan(dest ...any) error {
	return r.row.Scan(append(dest, &r.a.IdempotencyKey, &r.a.RequestHash)...)
}
func validateArchivedAttachments(archive *ScopeArchive, agents map[string]bool) error {
	if archive.Version < 3 && len(archive.Attachments) > 0 {
		return Errorf(CodeInvalidArgument, "Attachments require archive version 3")
	}
	seen := map[string]bool{}
	keys := map[string]bool{}
	var total int64
	if len(archive.Attachments) > 10000 {
		return Errorf(CodeInvalidArgument, "Too many archived attachments")
	}
	for _, a := range archive.Attachments {
		if !attachmentIDPattern.MatchString(a.ID) || seen[a.ID] {
			return Errorf(CodeInvalidArgument, "Archive contains invalid or duplicate attachment IDs")
		}
		seen[a.ID] = true
		input := UploadAttachmentInput{Name: a.Name, MediaType: a.MediaType, IdempotencyKey: a.IdempotencyKey}
		if err := validateAttachmentInput(&input); err != nil {
			return err
		}
		if a.MediaType != input.MediaType || a.Data == nil || a.Size != int64(len(a.Data)) || a.Size > MaxAttachmentBytes || a.URI != "october-bus://attachments/"+a.ID {
			return Errorf(CodeInvalidArgument, "Invalid archived attachment metadata")
		}
		sum := sha256.Sum256(a.Data)
		if a.SHA256 != hex.EncodeToString(sum[:]) {
			return Errorf(CodeInvalidArgument, "Archive attachment checksum mismatch")
		}
		if a.AuthorKind != MessageParticipantAgent && a.AuthorKind != MessageParticipantHuman {
			return Errorf(CodeInvalidArgument, "Invalid attachment author")
		}
		if err := validateIdentity(a.AuthorID, "authorId", false); err != nil {
			return err
		}
		if a.AuthorKind == MessageParticipantAgent && !agents[a.AuthorID] {
			return Errorf(CodeInvalidArgument, "Unknown attachment author")
		}
		if _, err := archiveTime(a.CreatedAt, true); err != nil {
			return err
		}
		hash, err := hex.DecodeString(a.RequestHash)
		if err != nil || len(hash) != 32 || a.RequestHash != attachmentRequestHash(input, a.SHA256) {
			return Errorf(CodeInvalidArgument, "Invalid attachment request hash")
		}
		if a.IdempotencyKey != "" {
			key := string(a.AuthorKind) + "\x00" + a.AuthorID + "\x00" + a.IdempotencyKey
			if keys[key] {
				return Errorf(CodeInvalidArgument, "Duplicate attachment retry key")
			}
			keys[key] = true
		}
		total += a.Size
		if total > attachmentStorageBytes {
			return Errorf(CodeInvalidArgument, "Archive attachment storage limit exceeded")
		}
	}
	return nil
}
func (s *Store) importAttachments(ctx context.Context, tx sqlExecutor, archive ScopeArchive) error {
	if len(archive.Attachments) == 0 {
		return nil
	}
	s.files.mu.Lock()
	defer s.files.mu.Unlock()
	if err := s.files.open(s.source); err != nil {
		return err
	}
	for _, a := range archive.Attachments {
		created, _ := archiveTime(a.CreatedAt, true)
		if _, err := tx.ExecContext(ctx, "INSERT INTO attachments(attachment_id,scope_id,author_kind,author_id,name,media_type,size_bytes,sha256,idempotency_key,request_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)", a.ID, archive.Scope.ID, a.AuthorKind, a.AuthorID, a.Name, a.MediaType, a.Size, a.SHA256, nullableString(a.IdempotencyKey), a.RequestHash, created); err != nil {
			return err
		}
		// A retry after a rolled-back import may find the already durable bytes.
		if _, err := s.files.root.Lstat(a.ID); err == nil {
			if _, err := s.readAttachmentFile(a.Attachment, 0, a.Size); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		} else if err := s.writeAttachmentFile(a.ID, a.Data); err != nil {
			return err
		}
	}
	return nil
}
