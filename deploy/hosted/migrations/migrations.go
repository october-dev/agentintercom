// Package migrations embeds the standalone hosted Bus schema. Migrations use
// only the october_bus namespace and never alter account or other product tables.
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS
