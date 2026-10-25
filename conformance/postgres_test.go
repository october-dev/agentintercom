package conformance_test

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/october-dev/october-bus/bus"
	"github.com/october-dev/october-bus/conformance"
)

func TestPostgresRuntimeProfile(t *testing.T) {
	dsn := os.Getenv("OCTOBER_BUS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set OCTOBER_BUS_TEST_POSTGRES_URL to an isolated local october_bus_test database")
	}
	u, err := url.Parse(dsn)
	require(t, err == nil && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") && u.Path == "/october_bus_test", "PostgreSQL tests require an isolated loopback october_bus_test database")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	requireNoError(t, bus.MigratePostgres(ctx, dsn))
	runtimeValue, err := bus.OpenPostgres(ctx, dsn)
	requireNoError(t, err)
	server := bus.NewServer(runtimeValue, bus.ServerOptions{AdminToken: "conformance-admin-token"})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())
	result, err := conformance.Run(ctx, conformance.Options{Address: address, AdminToken: "conformance-admin-token"})
	requireNoError(t, err)
	require(t, len(result.Passed) >= 10 && len(result.Failed) == 0, "unexpected result: %#v", result)
	t.Logf("PostgreSQL passed %d public HTTP/MCP contract checks", len(result.Passed))
}
