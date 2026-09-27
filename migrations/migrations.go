package migrations

import _ "embed"

//go:embed 001_initial.sql
var sql001 string

//go:embed 002_question_lists.sql
var sql002 string

//go:embed 003_themes.sql
var sql003 string

//go:embed 004_catalog_deletes.sql
var sql004 string

// SQL is the full migration script applied at startup.
// Every part is idempotent (IF NOT EXISTS, or guarded DO blocks) so the
// script can run on every start.
var SQL = sql001 + "\n" + sql002 + "\n" + sql003 + "\n" + sql004
