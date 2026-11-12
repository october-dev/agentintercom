package bus

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// SQL storage uses the same statements and domain transitions on both engines.
// Only placeholder syntax differs. Arguments are always bound by the driver.
type sqlDatabase struct {
	*sql.DB
	backend StorageBackend
}

type sqlTransaction struct {
	*sql.Tx
	backend StorageBackend
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func sqlStatement(backend StorageBackend, statement string) string {
	if backend != StorageBackendPostgres {
		return statement
	}
	var result strings.Builder
	result.Grow(len(statement) + 16)
	parameter := 0
	var quote byte
	for i := 0; i < len(statement); i++ {
		c := statement[i]
		if quote != 0 {
			result.WriteByte(c)
			if c == quote {
				if i+1 < len(statement) && statement[i+1] == quote {
					i++
					result.WriteByte(quote)
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
		}
		if c == '?' {
			parameter++
			result.WriteByte('$')
			result.WriteString(strconv.Itoa(parameter))
		} else {
			result.WriteByte(c)
		}
	}
	return result.String()
}

// The portable schema uses 0/1 integers for flags. SQLite accepts bool values
// directly; PostgreSQL requires us to bind their integer representation.
func sqlArguments(backend StorageBackend, args []any) []any {
	if backend != StorageBackendPostgres {
		return args
	}
	result := append([]any(nil), args...)
	for i, arg := range result {
		if value, ok := arg.(bool); ok {
			result[i] = 0
			if value {
				result[i] = 1
			}
		}
	}
	return result
}

func (d *sqlDatabase) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, sqlStatement(d.backend, query), sqlArguments(d.backend, args)...)
}
func (d *sqlDatabase) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, sqlStatement(d.backend, query), sqlArguments(d.backend, args)...)
}
func (d *sqlDatabase) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, sqlStatement(d.backend, query), sqlArguments(d.backend, args)...)
}
func (d *sqlDatabase) Exec(query string, args ...any) (sql.Result, error) {
	return d.ExecContext(context.Background(), query, args...)
}
func (d *sqlDatabase) Query(query string, args ...any) (*sql.Rows, error) {
	return d.QueryContext(context.Background(), query, args...)
}
func (d *sqlDatabase) QueryRow(query string, args ...any) *sql.Row {
	return d.QueryRowContext(context.Background(), query, args...)
}
func (d *sqlDatabase) BeginTx(ctx context.Context, options *sql.TxOptions) (*sqlTransaction, error) {
	tx, err := d.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &sqlTransaction{Tx: tx, backend: d.backend}, nil
}
func (t *sqlTransaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, sqlStatement(t.backend, query), sqlArguments(t.backend, args)...)
}
func (t *sqlTransaction) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, sqlStatement(t.backend, query), sqlArguments(t.backend, args)...)
}
func (t *sqlTransaction) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, sqlStatement(t.backend, query), sqlArguments(t.backend, args)...)
}
