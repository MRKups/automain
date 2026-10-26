package catalog

// ApplicationID distinguishes the metadata catalog from source and legacy vector files.
const ApplicationID = 0x414d4341

const schemaVersion = 1

const schemaSQL = `
CREATE TABLE catalog_schema_migrations (
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
CREATE INDEX chunks_repo_id ON chunks (repo, id);
CREATE TABLE embedding_spaces (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    revision TEXT NOT NULL,
    dimensions INTEGER NOT NULL CHECK (dimensions > 0 AND dimensions <= 65536),
    UNIQUE (provider, model, revision, dimensions)
);
CREATE TABLE index_entries (
    repo TEXT NOT NULL,
    space_id TEXT NOT NULL REFERENCES embedding_spaces(id),
    chunk_id TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    point_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'indexed', 'delete')),
    PRIMARY KEY (repo, space_id, chunk_id, content_hash),
    UNIQUE (repo, space_id, point_id)
);
CREATE INDEX index_entries_cleanup ON index_entries (repo, state, space_id, point_id);
CREATE INDEX index_entries_chunk ON index_entries (repo, chunk_id);
`
