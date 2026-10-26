package vector

// ApplicationID identifies the old SQLite vector format, never a source/catalog DB.
const ApplicationID = 1096173912

const schemaVersion = 1

// schemaSQL documents the historical format for migration fixtures. The reader
// never executes it, initializes a database, or applies schema migrations.
const schemaSQL = `
CREATE TABLE vector_schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);
CREATE TABLE chunks (
    id TEXT PRIMARY KEY,
    repo TEXT NOT NULL,
    item_number INTEGER NOT NULL CHECK (item_number > 0),
    item_kind TEXT NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('item_title', 'item_body', 'comment')),
    comment_id TEXT NOT NULL,
    start_byte INTEGER NOT NULL CHECK (start_byte >= 0),
    end_byte INTEGER NOT NULL CHECK (end_byte > start_byte),
    title TEXT NOT NULL,
    text TEXT NOT NULL,
    author TEXT NOT NULL,
    url TEXT NOT NULL,
    content_hash TEXT NOT NULL
);
CREATE INDEX chunks_repo_item ON chunks (repo, item_number);
CREATE TABLE embedding_spaces (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    revision TEXT NOT NULL,
    dimensions INTEGER NOT NULL CHECK (dimensions > 0),
    UNIQUE (provider, model, revision, dimensions)
);
CREATE TABLE embeddings (
    chunk_id TEXT NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    space_id TEXT NOT NULL REFERENCES embedding_spaces(id),
    content_hash TEXT NOT NULL,
    dimensions INTEGER NOT NULL CHECK (dimensions > 0),
    vector BLOB NOT NULL CHECK (
        vec_type(vector) = 'float32' AND vec_length(vector) = dimensions
    ),
    PRIMARY KEY (space_id, chunk_id)
);
CREATE INDEX embeddings_chunk ON embeddings (chunk_id);
CREATE TRIGGER invalidate_changed_chunk AFTER UPDATE OF content_hash ON chunks
WHEN OLD.content_hash != NEW.content_hash
BEGIN
    DELETE FROM embeddings WHERE chunk_id = NEW.id;
END;
`
