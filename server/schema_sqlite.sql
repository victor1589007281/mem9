-- Control plane schema (SQLite).

CREATE TABLE IF NOT EXISTS tenants (
  id              TEXT     PRIMARY KEY,
  name            TEXT     NOT NULL,
  db_host         TEXT     NOT NULL,
  db_port         INTEGER  NOT NULL,
  db_user         TEXT     NOT NULL,
  db_password     TEXT     NOT NULL,
  db_name         TEXT     NOT NULL,
  db_tls          INTEGER  NOT NULL DEFAULT 0,
  provider        TEXT     NOT NULL,
  cluster_id      TEXT     NULL,
  claim_url       TEXT     NULL,
  claim_expires_at TEXT    NULL,
  status          TEXT     NOT NULL DEFAULT 'provisioning',
  schema_version  INTEGER  NOT NULL DEFAULT 1,
  created_at      TEXT     DEFAULT (datetime('now')),
  updated_at      TEXT     DEFAULT (datetime('now')),
  deleted_at      TEXT     NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_name ON tenants(name);
CREATE INDEX IF NOT EXISTS idx_tenant_status ON tenants(status);
CREATE INDEX IF NOT EXISTS idx_tenant_provider ON tenants(provider);

CREATE TABLE IF NOT EXISTS tenant_tokens (
  api_token     TEXT     PRIMARY KEY,
  tenant_id     TEXT     NOT NULL,
  created_at    TEXT     DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_tenant ON tenant_tokens(tenant_id);

-- Upload task tracking (control plane).
CREATE TABLE IF NOT EXISTS upload_tasks (
  task_id       TEXT     PRIMARY KEY,
  tenant_id     TEXT     NOT NULL,
  file_name     TEXT     NOT NULL,
  file_path     TEXT     NOT NULL,
  agent_id      TEXT     NULL,
  session_id    TEXT     NULL,
  file_type     TEXT     NOT NULL,
  total_chunks  INTEGER  NOT NULL DEFAULT 0,
  done_chunks   INTEGER  NOT NULL DEFAULT 0,
  status        TEXT     NOT NULL DEFAULT 'pending',
  error_msg     TEXT     NULL,
  created_at    TEXT     DEFAULT (datetime('now')),
  updated_at    TEXT     DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_upload_tenant ON upload_tasks(tenant_id);
CREATE INDEX IF NOT EXISTS idx_upload_poll ON upload_tasks(status, created_at);

-- Tenant data plane schema (per-tenant SQLite file).
-- Embedding stored as TEXT in JSON array format "[0.1,0.2,...]".
-- Cosine distance computed in application code.

CREATE TABLE IF NOT EXISTS memories (
  id              TEXT     PRIMARY KEY,
  content         TEXT     NOT NULL,
  source          TEXT,
  tags            TEXT,
  metadata        TEXT,
  embedding       TEXT     NULL,

  memory_type     TEXT     NOT NULL DEFAULT 'pinned',
  agent_id        TEXT     NULL,
  session_id      TEXT     NULL,
  state           TEXT     NOT NULL DEFAULT 'active',
  version         INTEGER  DEFAULT 1,
  updated_by      TEXT,
  created_at      TEXT     DEFAULT (datetime('now')),
  updated_at      TEXT     DEFAULT (datetime('now')),
  superseded_by   TEXT     NULL
);
CREATE INDEX IF NOT EXISTS idx_memory_type ON memories(memory_type);
CREATE INDEX IF NOT EXISTS idx_source ON memories(source);
CREATE INDEX IF NOT EXISTS idx_state ON memories(state);
CREATE INDEX IF NOT EXISTS idx_agent ON memories(agent_id);
CREATE INDEX IF NOT EXISTS idx_session ON memories(session_id);
CREATE INDEX IF NOT EXISTS idx_updated ON memories(updated_at);

-- FTS5 virtual table for full-text search.
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(content, id UNINDEXED);
