package migrations

import "embed"

// FS holds the SQL migration files, embedded into the binary so the server can
// apply the schema on startup regardless of how the database was provisioned.
//
//go:embed *.sql
var FS embed.FS
