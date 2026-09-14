package postgres

import "embed"

//go:embed migrations/*.sql
var migrationsFS embed.FS
