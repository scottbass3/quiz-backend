package migrations

import _ "embed"

// SQL is the full migration script, embedded at compile time.
// All statements use IF NOT EXISTS so the script is safe to run multiple times.
//
//go:embed 001_initial.sql
var SQL string
