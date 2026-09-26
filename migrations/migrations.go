package migrations

import _ "embed"

//go:embed 001_initial.sql
var sql001 string

//go:embed 002_question_lists.sql
var sql002 string

//go:embed 003_themes.sql
var sql003 string

// SQL is the full migration script applied at startup.
// Every part uses IF NOT EXISTS / ADD COLUMN IF NOT EXISTS so the script is idempotent.
var SQL = sql001 + "\n" + sql002 + "\n" + sql003
