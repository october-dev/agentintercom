package bus

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestDatabaseContentionIsSafeBackpressure(t *testing.T) {
	for _, code := range []string{"55P03", "23505", "unsafe\ncredential"} {
		err := fmt.Errorf("database operation: %w", &pgconn.PgError{Code: code, Message: "private credential", Detail: "private workspace data"})
		failure := AsBusError(err)
		if code == "55P03" {
			require(t, failure.Code == CodeBackpressure && ErrorStatus(err) == http.StatusTooManyRequests, "lock contention must be retryable")
		} else {
			require(t, failure.Code == CodeInternal && failure.Message == "Internal October Bus error", "driver details escaped")
		}
		if len(code) != 5 {
			require(t, databaseErrorState(err) == "", "untrusted diagnostic text accepted")
		} else {
			require(t, databaseErrorState(err) == code, "missing SQLSTATE")
		}
	}
}
