package db

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationsTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
)`

// MigrationFiles lists the .sql files in fsys in apply order. Filenames carry a
// numeric prefix, so lexical order is apply order.
func MigrationFiles(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Migrate applies every not-yet-recorded migration and returns the ones it ran.
//
// This needs DDL and CREATE ROLE rights, so it runs on an admin pool - not the
// NOSUPERUSER application pool that serves requests.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]string, error) {
	if _, err := pool.Exec(ctx, migrationsTableDDL); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}
	names, err := MigrationFiles(fsys)
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var ran []string
	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return ran, fmt.Errorf("read %s: %w", name, err)
		}
		// One transaction per file: a failed migration leaves no partial schema.
		tx, err := pool.Begin(ctx)
		if err != nil {
			return ran, fmt.Errorf("begin %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return ran, fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return ran, fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return ran, fmt.Errorf("commit %s: %w", name, err)
		}
		ran = append(ran, name)
	}
	return ran, nil
}
