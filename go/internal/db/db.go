// Package db provides SQLite database access for Odysseus.
//
// Uses mattn/go-sqlite3 (CGO) for SQLite access. The database is opened
// in WAL mode so both Go and Python can read/write concurrently during
// the transition period.
package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

// DB wraps a *sql.DB with Odysseus-specific helpers.
type DB struct {
	*sql.DB
	path string
}

var (
	instance *DB
	once     sync.Once
)

// Open opens (or creates) the SQLite database at the given path.
// Enables WAL mode, foreign keys, and a busy timeout for concurrent access.
func Open(dataDir string) (*DB, error) {
	dbPath := filepath.Join(dataDir, "app.db")

	// Ensure data directory exists.
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("db: mkdir %s: %w", dataDir, err)
	}

	// DSN flags: WAL mode, foreign keys, 5s busy timeout.
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_foreign_keys=ON&_busy_timeout=5000", dbPath)

	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", dbPath, err)
	}

	// Verify the connection works and WAL is active.
	var journalMode string
	if err := sqlDB.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: pragma check: %w", err)
	}
	slog.Info("db: opened", "path", dbPath, "journal_mode", journalMode)

	// SQLite is single-writer; limit to 1 writer + many readers.
	sqlDB.SetMaxOpenConns(4)

	d := &DB{DB: sqlDB, path: dbPath}
	return d, nil
}

// MustOpen calls Open and panics on error.
func MustOpen(dataDir string) *DB {
	d, err := Open(dataDir)
	if err != nil {
		panic(err)
	}
	return d
}

// Path returns the database file path.
func (d *DB) Path() string {
	return d.path
}

// Tx executes fn inside a transaction. Rolls back on error or panic.
func (d *DB) Tx(fn func(tx *sql.Tx) error) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// NullString returns a sql.NullString: valid if s is non-empty.
func NullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// NullInt64 returns a sql.NullInt64: valid if n is non-zero.
func NullInt64(n int64) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: n, Valid: true}
}

// StringVal returns the string value or empty string if null.
func StringVal(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// Int64Val returns the int64 value or 0 if null.
func Int64Val(ni sql.NullInt64) int64 {
	if ni.Valid {
		return ni.Int64
	}
	return 0
}
