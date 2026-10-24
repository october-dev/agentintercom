package bus

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Backup writes a consistent SQLite snapshot, including committed WAL contents.
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
	http.ServeContent(response, request, "october-bus.db", info.ModTime(), file)
	return nil
}

// BackupTo streams a full database snapshot without the portable JSON size limit.
func (c Client) BackupTo(ctx context.Context, destination io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Address+"/v1/admin/backup", nil)
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
