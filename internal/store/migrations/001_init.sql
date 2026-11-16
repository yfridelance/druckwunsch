CREATE TABLE users (
    id          INTEGER PRIMARY KEY,
    oidc_sub    TEXT NOT NULL UNIQUE,
    email       TEXT NOT NULL DEFAULT '',
    name        TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE sessions (
    token       TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  DATETIME NOT NULL
);

CREATE TABLE models (
    id               INTEGER PRIMARY KEY,
    platform         TEXT NOT NULL,
    source_id        TEXT NOT NULL DEFAULT '',
    url              TEXT NOT NULL UNIQUE,
    title            TEXT NOT NULL DEFAULT '',
    designer         TEXT NOT NULL DEFAULT '',
    designer_url     TEXT NOT NULL DEFAULT '',
    description      TEXT NOT NULL DEFAULT '',
    license          TEXT NOT NULL DEFAULT '',
    cover_image      TEXT NOT NULL DEFAULT '',
    images           TEXT NOT NULL DEFAULT '[]',
    raw_json         TEXT NOT NULL DEFAULT '',
    metadata_status  TEXT NOT NULL DEFAULT 'fehlt',
    fetched_at       DATETIME,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE profiles (
    id                 INTEGER PRIMARY KEY,
    model_id           INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    source_profile_id  TEXT NOT NULL,
    name               TEXT NOT NULL DEFAULT '',
    printer            TEXT NOT NULL DEFAULT '',
    nozzle             TEXT NOT NULL DEFAULT '',
    print_time_sec     INTEGER NOT NULL DEFAULT 0,
    filament_g         REAL NOT NULL DEFAULT 0,
    plate_count        INTEGER NOT NULL DEFAULT 0,
    materials          TEXT NOT NULL DEFAULT '[]',
    cover_image        TEXT NOT NULL DEFAULT '',
    url                TEXT NOT NULL DEFAULT '',
    UNIQUE (model_id, source_profile_id)
);

CREATE TABLE lists (
    id          INTEGER PRIMARY KEY,
    owner_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    is_inbox    INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE list_members (
    list_id  INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role     TEXT NOT NULL CHECK (role IN ('viewer', 'editor')),
    PRIMARY KEY (list_id, user_id)
);

CREATE TABLE items (
    id                 INTEGER PRIMARY KEY,
    model_id           INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    list_id            INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    added_by           INTEGER NOT NULL REFERENCES users(id),
    requested_by       TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'neu',
    chosen_profile_id  INTEGER REFERENCES profiles(id) ON DELETE SET NULL,
    quantity           INTEGER NOT NULL DEFAULT 1,
    color              TEXT NOT NULL DEFAULT '',
    priority           INTEGER NOT NULL DEFAULT 0,
    note               TEXT NOT NULL DEFAULT '',
    printed_at         DATETIME,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX items_list ON items(list_id);
CREATE INDEX items_model ON items(model_id);

CREATE TABLE labels (
    id     INTEGER PRIMARY KEY,
    name   TEXT NOT NULL UNIQUE,
    color  TEXT NOT NULL DEFAULT '#888888'
);

CREATE TABLE item_labels (
    item_id   INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    label_id  INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, label_id)
);

CREATE TABLE comments (
    id          INTEGER PRIMARY KEY,
    item_id     INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    text        TEXT NOT NULL,
    is_system   INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Payloads posted by the bookmarklet before the user confirms them.
CREATE TABLE pending_ingest (
    token       TEXT PRIMARY KEY,
    payload     TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
