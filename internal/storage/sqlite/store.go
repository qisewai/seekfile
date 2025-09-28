package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"seekfile/internal/storage"

	_ "modernc.org/sqlite"
)

// Store persists file metadata inside a SQLite database.
type Store struct {
	db *sql.DB
}

// Open initializes (or reuses) a SQLite database at the provided path.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path cannot be empty")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, pragma := range pragmas {
		if _, execErr := db.Exec(pragma); execErr != nil {
			db.Close()
			return nil, fmt.Errorf("apply pragma %q: %w", pragma, execErr)
		}
	}

	store := &Store{db: db}
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, err
	}

	return store, nil
}

// Close releases the underlying database resources.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) initSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS file_records (
        path TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        size INTEGER NOT NULL,
        mod_time INTEGER NOT NULL,
        root_path TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS scan_state (
        root_path TEXT PRIMARY KEY,
        last_full_scan INTEGER NOT NULL DEFAULT 0,
        last_incremental_scan INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_file_records_root ON file_records(root_path);
`

	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	return nil
}

// LoadAll retrieves every persisted record.
func (s *Store) LoadAll(ctx context.Context) ([]storage.Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, name, size, mod_time, root_path FROM file_records`)
	if err != nil {
		return nil, fmt.Errorf("query records: %w", err)
	}
	defer rows.Close()

	var records []storage.Record
	for rows.Next() {
		var (
			path    string
			name    string
			size    int64
			modTime int64
			root    string
		)
		if scanErr := rows.Scan(&path, &name, &size, &modTime, &root); scanErr != nil {
			return nil, fmt.Errorf("scan record: %w", scanErr)
		}

		record := storage.Record{
			Path:     path,
			Name:     name,
			Size:     size,
			ModTime:  time.Unix(0, modTime),
			RootPath: root,
		}
		records = append(records, record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate records: %w", err)
	}

	return records, nil
}

// Upsert inserts or updates a record.
func (s *Store) Upsert(ctx context.Context, record storage.Record) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO file_records(path, name, size, mod_time, root_path)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET
        name=excluded.name,
        size=excluded.size,
        mod_time=excluded.mod_time,
        root_path=excluded.root_path
`, record.Path, record.Name, record.Size, record.ModTime.UnixNano(), record.RootPath)
	if err != nil {
		return fmt.Errorf("upsert record %s: %w", record.Path, err)
	}
	return nil
}

// Delete removes a record by its path.
func (s *Store) Delete(ctx context.Context, path string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM file_records WHERE path = ?`, path); err != nil {
		return fmt.Errorf("delete record %s: %w", path, err)
	}
	return nil
}

// ScanState retrieves the last known scan state for a root path.
func (s *Store) ScanState(ctx context.Context, root string) (storage.ScanState, error) {
	var (
		lastFull        int64
		lastIncremental int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT last_full_scan, last_incremental_scan FROM scan_state WHERE root_path = ?
`, root).Scan(&lastFull, &lastIncremental)

	if errors.Is(err, sql.ErrNoRows) {
		return storage.ScanState{RootPath: root}, nil
	}
	if err != nil {
		return storage.ScanState{}, fmt.Errorf("query scan state: %w", err)
	}

	return storage.ScanState{
		RootPath:            root,
		LastFullScan:        time.Unix(0, lastFull),
		LastIncrementalScan: time.Unix(0, lastIncremental),
	}, nil
}

// UpdateScanState writes the scan timestamps for a root path.
func (s *Store) UpdateScanState(ctx context.Context, state storage.ScanState) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO scan_state(root_path, last_full_scan, last_incremental_scan)
VALUES(?, ?, ?)
ON CONFLICT(root_path) DO UPDATE SET
        last_full_scan=excluded.last_full_scan,
        last_incremental_scan=excluded.last_incremental_scan
`, state.RootPath, state.LastFullScan.UnixNano(), state.LastIncrementalScan.UnixNano())
	if err != nil {
		return fmt.Errorf("update scan state %s: %w", state.RootPath, err)
	}
	return nil
}
