package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type migration struct {
	version int
	sql     string
}

var schemaMigrations = []migration{
	{
		version: 1,
		sql: `
CREATE TABLE IF NOT EXISTS repositories (
    repo TEXT PRIMARY KEY,
    last_synced_at TEXT DEFAULT '',
    cursor TEXT DEFAULT '',
    sync_status TEXT NOT NULL DEFAULT 'idle',
    updated_at TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS items (
    repo TEXT NOT NULL,
    number INTEGER NOT NULL CHECK (number > 0),
    external_id TEXT DEFAULT '',
    kind TEXT NOT NULL CHECK (kind IN ('issue', 'pull_request', 'discussion')),
    state TEXT NOT NULL CHECK (state IN ('open', 'closed', 'merged', 'answered')),
    title TEXT NOT NULL,
    body TEXT DEFAULT '',
    author TEXT DEFAULT '',
    labels_json TEXT NOT NULL DEFAULT '[]',
    url TEXT DEFAULT '',
    created_at TEXT DEFAULT '',
    updated_at TEXT DEFAULT '',
    content_hash TEXT NOT NULL,
    last_analyzed_hash TEXT DEFAULT '',
    PRIMARY KEY (repo, number)
);

CREATE INDEX IF NOT EXISTS idx_items_repo_dirty
    ON items (repo, content_hash, last_analyzed_hash);

CREATE INDEX IF NOT EXISTS idx_items_repo_kind_state
    ON items (repo, kind, state);

CREATE TABLE IF NOT EXISTS comments (
    repo TEXT NOT NULL,
    comment_id TEXT NOT NULL,
    item_number INTEGER NOT NULL,
    author TEXT DEFAULT '',
    body TEXT DEFAULT '',
    url TEXT DEFAULT '',
    created_at TEXT DEFAULT '',
    updated_at TEXT DEFAULT '',
    PRIMARY KEY (repo, comment_id),
    FOREIGN KEY (repo, item_number) REFERENCES items (repo, number) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_comments_repo_item
    ON comments (repo, item_number, created_at);

CREATE TABLE IF NOT EXISTS threads (
    id TEXT PRIMARY KEY,
    repo TEXT NOT NULL,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'resolved', 'stale')),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TEXT DEFAULT '',
    updated_at TEXT DEFAULT '',
    UNIQUE (repo, id)
);

CREATE INDEX IF NOT EXISTS idx_threads_repo_status_updated
    ON threads (repo, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS relationships (
    id TEXT PRIMARY KEY,
    repo TEXT NOT NULL,
    thread_id TEXT NOT NULL,
    item_number INTEGER NOT NULL,
    rel_type TEXT NOT NULL CHECK (rel_type IN (
        'reports_same_problem',
        'proposes_fix',
        'implements_decision',
        'blocks_or_depends',
        'related_context',
        'duplicate_of'
    )),
    explanation TEXT NOT NULL,
    confidence TEXT NOT NULL DEFAULT 'medium' CHECK (confidence IN ('high', 'medium', 'low')),
    explicitly_linked INTEGER NOT NULL DEFAULT 0,
    verified_by_human INTEGER NOT NULL DEFAULT 0,
    created_at TEXT DEFAULT '',
    updated_at TEXT DEFAULT '',
    UNIQUE (repo, thread_id, item_number, rel_type),
    FOREIGN KEY (repo, thread_id) REFERENCES threads (repo, id) ON DELETE CASCADE,
    FOREIGN KEY (repo, item_number) REFERENCES items (repo, number) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_relationships_repo_thread
    ON relationships (repo, thread_id);

CREATE INDEX IF NOT EXISTS idx_relationships_repo_item
    ON relationships (repo, item_number);

CREATE INDEX IF NOT EXISTS idx_relationships_repo_type_verified
    ON relationships (repo, rel_type, verified_by_human);

CREATE TABLE IF NOT EXISTS relationship_evidence (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    relationship_id TEXT NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('item_body', 'comment')),
    item_number INTEGER NOT NULL CHECK (item_number > 0),
    comment_id TEXT DEFAULT '',
    author TEXT DEFAULT '',
    excerpt TEXT NOT NULL,
    url TEXT DEFAULT '',
    FOREIGN KEY (relationship_id) REFERENCES relationships (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_relationship_evidence_rel_id
    ON relationship_evidence (relationship_id, id);
`,
	},
}

func migrate(ctx context.Context, db *sql.DB) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin migration transaction: %w", err)
	}
	defer rollbackTx(tx)

	const createTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);`
	if _, err := tx.ExecContext(ctx, createTableSQL); err != nil {
		return 0, fmt.Errorf("create schema_migrations table: %w", err)
	}

	var currentVersion int
	row := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations")
	if err := row.Scan(&currentVersion); err != nil {
		return 0, fmt.Errorf("read current schema version: %w", err)
	}

	now := formatTime(time.Now().UTC())
	for _, m := range schemaMigrations {
		if m.version <= currentVersion {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return currentVersion, fmt.Errorf("execute migration v%d: %w", m.version, err)
		}

		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			m.version,
			now,
		); err != nil {
			return currentVersion, fmt.Errorf("record migration v%d: %w", m.version, err)
		}
		currentVersion = m.version
	}

	if err := tx.Commit(); err != nil {
		return currentVersion, fmt.Errorf("commit schema migrations: %w", err)
	}

	return currentVersion, nil
}
