package bus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Backup writes a consistent SQLite database snapshot, including committed WAL
// contents. Attachment bytes are separate; BackupBundle includes them.
// The destination must be new or empty. Credentials are included: keep it private.
func (s *Store) Backup(ctx context.Context, path string) error {
	if s.Backend() == StorageBackendPostgres {
		return Errorf(CodeInvalidArgument, "Use pg_dump or the managed database backup service for PostgreSQL; scope export remains available")
	}
	_, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

func (s *Server) backupDatabase(response http.ResponseWriter, request *http.Request) error {
	if err := s.requireAdmin(request); err != nil {
		return err
	}
	if request.URL.Query().Get("bundle") == "1" {
		return s.backupBundle(response, request)
	}
	file, err := os.CreateTemp("", "october-bus-backup-*.db")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Minute)
	defer cancel()
	_ = http.NewResponseController(response).SetWriteDeadline(time.Now().Add(5 * time.Minute))
	if err := s.runtime.store.Backup(ctx, path); err != nil {
		return err
	}
	file, err = os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	response.Header().Set("Content-Type", "application/vnd.sqlite3")
	response.Header().Set("Content-Disposition", `attachment; filename="october-bus.db"`)
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-October-Bus-Backup-Includes", "database-only")
	http.ServeContent(response, request, "october-bus.db", info.ModTime(), file)
	return nil
}

// BackupTo streams a full database snapshot without the portable JSON size limit.
func (c Client) BackupTo(ctx context.Context, destination io.Writer) error {
	return c.backupTo(ctx, destination, false)
}

func (c Client) BackupBundleTo(ctx context.Context, destination io.Writer) error {
	return c.backupTo(ctx, destination, true)
}
func (c Client) backupTo(ctx context.Context, destination io.Writer, bundle bool) error {
	path := "/v1/admin/backup"
	if bundle {
		path += "?bundle=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Address+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	response, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("database backup failed with HTTP %d", response.StatusCode)
	}
	_, err = io.Copy(destination, response.Body)
	return err
}

// BackupBundle snapshots SQLite first and includes exactly its live attachment
// files. Immutable bytes and the deletion lock keep the two parts consistent.
func (s *Store) BackupBundle(ctx context.Context, output io.Writer) error {
	s.files.maintenance.RLock()
	defer s.files.maintenance.RUnlock()
	if s.Backend() != StorageBackendSQLite {
		return Errorf(CodeInvalidArgument, "PostgreSQL backups must include pg_dump and the private attachment directory")
	}
	dir, err := os.MkdirTemp("", "october-bus-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "bus.db")
	if err := s.Backup(ctx, dbPath); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT "+attachmentColumns+" FROM attachments WHERE deleted_at=0 ORDER BY attachment_id")
	if err != nil {
		return err
	}
	defer rows.Close()
	s.files.mu.Lock()
	defer s.files.mu.Unlock()
	z := gzip.NewWriter(output)
	defer z.Close()
	tarball := tar.NewWriter(z)
	write := func(name string, size int64, reader io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := tarball.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: size, ModTime: time.Now().UTC(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := io.CopyN(tarball, reader, size)
		return err
	}
	manifest, _ := json.Marshal(map[string]any{"format": "october-bus.backup", "version": 1, "database": "bus.db", "attachments": "attachments/"})
	if err := write("manifest.json", int64(len(manifest)), strings.NewReader(string(manifest))); err != nil {
		return err
	}
	file, err := os.Open(dbPath)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	err = write("bus.db", info.Size(), file)
	file.Close()
	if err != nil {
		return err
	}
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return err
		}
		if err := s.files.open(s.source); err != nil {
			return err
		}
		data, err := s.readAttachmentFile(a, 0, a.Size)
		if err != nil {
			return err
		}
		if err := write("attachments/"+a.ID, a.Size, bytes.NewReader(data)); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := tarball.Close(); err != nil {
		return err
	}
	return z.Close()
}
func (s *Server) backupBundle(w http.ResponseWriter, r *http.Request) error {
	file, err := os.CreateTemp("", "october-bus-bundle-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute))
	if err := s.runtime.store.BackupBundle(ctx, file); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="october-bus.tar.gz"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-October-Bus-Backup-Includes", "database-and-attachments")
	http.ServeContent(w, r, "october-bus.tar.gz", info.ModTime(), file)
	return nil
}
