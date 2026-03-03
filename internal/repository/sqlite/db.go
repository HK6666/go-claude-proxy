package sqlite

import (
	"database/sql"
	"encoding/json"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	db *sql.DB
}

func NewDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, err
	}

	d := &DB{db: db}
	if err := d.migrate(); err != nil {
		return nil, err
	}

	return d, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS providers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		type TEXT NOT NULL,
		name TEXT NOT NULL,
		config TEXT,
		supported_client_types TEXT,
		deleted_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS projects (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		name TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		session_id TEXT NOT NULL UNIQUE,
		client_type TEXT NOT NULL,
		project_id INTEGER DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS routes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		is_enabled INTEGER DEFAULT 1,
		is_native INTEGER DEFAULT 1,
		project_id INTEGER DEFAULT 0,
		client_type TEXT NOT NULL,
		provider_id INTEGER NOT NULL,
		position INTEGER DEFAULT 0,
		retry_config_id INTEGER DEFAULT 0,
		model_mapping TEXT
	);

	CREATE TABLE IF NOT EXISTS retry_configs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		name TEXT NOT NULL,
		is_default INTEGER DEFAULT 0,
		max_retries INTEGER DEFAULT 3,
		initial_interval_ms INTEGER DEFAULT 1000,
		backoff_rate REAL DEFAULT 2.0,
		max_interval_ms INTEGER DEFAULT 30000
	);

	CREATE TABLE IF NOT EXISTS routing_strategies (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		project_id INTEGER DEFAULT 0,
		type TEXT NOT NULL,
		config TEXT
	);

	CREATE TABLE IF NOT EXISTS proxy_requests (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		instance_id TEXT,
		request_id TEXT,
		session_id TEXT,
		client_type TEXT,
		request_model TEXT,
		response_model TEXT,
		start_time DATETIME,
		end_time DATETIME,
		duration_ms INTEGER,
		status TEXT,
		request_info TEXT,
		response_info TEXT,
		error TEXT,
		proxy_upstream_attempt_count INTEGER DEFAULT 0,
		final_proxy_upstream_attempt_id INTEGER DEFAULT 0,
		input_token_count INTEGER DEFAULT 0,
		output_token_count INTEGER DEFAULT 0,
		cache_read_count INTEGER DEFAULT 0,
		cache_write_count INTEGER DEFAULT 0,
		cache_5m_write_count INTEGER DEFAULT 0,
		cache_1h_write_count INTEGER DEFAULT 0,
		cost INTEGER DEFAULT 0,
		route_id INTEGER DEFAULT 0,
		provider_id INTEGER DEFAULT 0,
		is_stream INTEGER DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS proxy_upstream_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		status TEXT,
		proxy_request_id INTEGER,
		request_info TEXT,
		response_info TEXT,
		route_id INTEGER,
		provider_id INTEGER,
		input_token_count INTEGER DEFAULT 0,
		output_token_count INTEGER DEFAULT 0,
		cache_read_count INTEGER DEFAULT 0,
		cache_write_count INTEGER DEFAULT 0,
		cache_5m_write_count INTEGER DEFAULT 0,
		cache_1h_write_count INTEGER DEFAULT 0,
		cost INTEGER DEFAULT 0,
		is_stream INTEGER DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS system_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_sessions_session_id ON sessions(session_id);
	CREATE INDEX IF NOT EXISTS idx_routes_project_client ON routes(project_id, client_type);
	CREATE INDEX IF NOT EXISTS idx_proxy_requests_session ON proxy_requests(session_id);

	CREATE TABLE IF NOT EXISTS cooldowns (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		provider_id INTEGER NOT NULL,
		client_type TEXT NOT NULL DEFAULT '',
		until_time DATETIME NOT NULL,
		reason TEXT NOT NULL DEFAULT 'unknown'
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_cooldowns_provider_client ON cooldowns(provider_id, client_type);
	CREATE INDEX IF NOT EXISTS idx_cooldowns_until ON cooldowns(until_time);

	CREATE TABLE IF NOT EXISTS failure_counts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		provider_id INTEGER NOT NULL,
		client_type TEXT NOT NULL DEFAULT '',
		reason TEXT NOT NULL,
		count INTEGER DEFAULT 0,
		last_failure_at DATETIME NOT NULL
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_failure_counts_provider_client_reason ON failure_counts(provider_id, client_type, reason);
	CREATE INDEX IF NOT EXISTS idx_failure_counts_last_failure ON failure_counts(last_failure_at);

	CREATE TABLE IF NOT EXISTS antigravity_quotas (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		email TEXT NOT NULL UNIQUE,
		subscription_tier TEXT DEFAULT 'FREE',
		is_forbidden INTEGER DEFAULT 0,
		models TEXT,
		last_updated INTEGER DEFAULT 0,
		name TEXT DEFAULT '',
		picture TEXT DEFAULT '',
		project_id TEXT DEFAULT ''
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_antigravity_quotas_email ON antigravity_quotas(email);
	`

	_, err := d.db.Exec(schema)
	if err != nil {
		return err
	}

	// Migration: Add reason column to cooldowns if it doesn't exist
	// Check if reason column exists
	var hasReason bool
	row := d.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('cooldowns') WHERE name='reason'`)
	row.Scan(&hasReason)

	if !hasReason {
		_, err = d.db.Exec(`ALTER TABLE cooldowns ADD COLUMN reason TEXT NOT NULL DEFAULT 'unknown'`)
		if err != nil {
			return err
		}
	}

	return nil
}

// Helper functions for JSON serialization
func toJSON(v interface{}) string {
	if v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func fromJSON[T any](s string) T {
	var v T
	if s != "" {
		json.Unmarshal([]byte(s), &v)
	}
	return v
}

func parseTime(t sql.NullTime) time.Time {
	if t.Valid {
		return t.Time
	}
	return time.Time{}
}

// parseTime parses a string timestamp into time.Time
func parseTimeString(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	// SQLite stores timestamps in RFC3339 format
	return time.Parse("2006-01-02 15:04:05", s)
}

// formatTime formats a time.Time into a string for SQLite
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}
