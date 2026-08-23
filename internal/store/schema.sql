-- vibepanel schema, version 1.
--
-- Applied by store.Open through a user_version check. Migrations are additive
-- files, never edits to this one: a released binary must be able to open a
-- database written by an older release without a manual step.

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

-- A project is a directory the user has named. Sessions belong to exactly one.
CREATE TABLE IF NOT EXISTS projects (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    path            TEXT NOT NULL,
    -- Manual ordering. NULL means "sort me automatically by activity", which
    -- is the default the user asked for; a number pins the row to a position.
    sort_index      INTEGER,
    pinned          INTEGER NOT NULL DEFAULT 0,
    last_active_at  INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_projects_activity ON projects(last_active_at DESC);

-- A session is one coding task: one tmux session, one pane, one agent.
CREATE TABLE IF NOT EXISTS sessions (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- The tmux session name on our dedicated socket. Unique because it is the
    -- real identity; the row is only bookkeeping around it.
    tmux_name       TEXT NOT NULL UNIQUE,
    title           TEXT NOT NULL DEFAULT '',
    -- 'auto' means the title tracks the pane title the application sets via
    -- OSC 0/2; 'manual' means the user renamed it and automatic updates must
    -- stop overwriting their choice.
    title_source    TEXT NOT NULL DEFAULT 'auto',
    state           TEXT NOT NULL DEFAULT 'done',
    state_source    TEXT NOT NULL DEFAULT 'heuristic',
    state_changed_at INTEGER NOT NULL DEFAULT 0,
    pinned          INTEGER NOT NULL DEFAULT 0,
    sort_index      INTEGER,
    cwd             TEXT NOT NULL DEFAULT '',
    command         TEXT NOT NULL DEFAULT '',
    -- Grid size last agreed with the controlling viewer. Restored on restart
    -- so a reconnecting browser does not briefly reflow the agent's TUI.
    cols            INTEGER NOT NULL DEFAULT 120,
    rows            INTEGER NOT NULL DEFAULT 32,
    last_output_at  INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    -- Set when the tmux session is gone but the user has not dismissed the row.
    archived_at     INTEGER
);

CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_id);
CREATE INDEX IF NOT EXISTS idx_sessions_state ON sessions(state);

-- Scratch terminals that follow the main session they were opened under.
CREATE TABLE IF NOT EXISTS bottom_terminals (
    id                TEXT PRIMARY KEY,
    parent_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tmux_name         TEXT NOT NULL UNIQUE,
    title             TEXT NOT NULL DEFAULT '',
    title_source      TEXT NOT NULL DEFAULT 'auto',
    sort_index        INTEGER NOT NULL DEFAULT 0,
    cols              INTEGER NOT NULL DEFAULT 120,
    rows              INTEGER NOT NULL DEFAULT 12,
    created_at        INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_bottom_parent ON bottom_terminals(parent_session_id);

-- One markdown note per project.
CREATE TABLE IF NOT EXISTS notes (
    project_id  TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    content_md  TEXT NOT NULL DEFAULT '',
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS todos (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    text        TEXT NOT NULL,
    done        INTEGER NOT NULL DEFAULT 0,
    sort_index  INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    done_at     INTEGER
);

CREATE INDEX IF NOT EXISTS idx_todos_project ON todos(project_id, sort_index);

-- Single-user today, but the schema carries a users table so that adding a
-- second account later is a migration rather than a rewrite of every auth path.
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL
);

-- WebAuthn credentials. A user may register several (laptop, phone).
CREATE TABLE IF NOT EXISTS credentials (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id BLOB NOT NULL UNIQUE,
    public_key    BLOB NOT NULL,
    -- Monotonic counter from the authenticator. A value that goes backwards
    -- means the credential was cloned, which we refuse rather than log.
    sign_count    INTEGER NOT NULL DEFAULT 0,
    transports    TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER
);

CREATE INDEX IF NOT EXISTS idx_credentials_user ON credentials(user_id);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
