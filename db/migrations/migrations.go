// Package migrations embeds the versioned SQL migrations of the PostgreSQL
// schema (goose format: NNNNN_description.sql with "-- +goose Up" and
// "-- +goose Down" sections). They are applied by `api migrate` (see
// docs/database.md); the SQL files are also the database creation script
// (deliverable D2).
package migrations

import "embed"

// FS holds the migration files at its root.
//
//go:embed *.sql
var FS embed.FS
