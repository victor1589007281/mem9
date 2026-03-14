-- Control plane schema (PostgreSQL).
-- Requires: CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS tenants (
  id              VARCHAR(36)   PRIMARY KEY,
  name            VARCHAR(255)  NOT NULL,
  db_host         VARCHAR(255)  NOT NULL,
  db_port         INT           NOT NULL,
  db_user         VARCHAR(255)  NOT NULL,
  db_password     VARCHAR(255)  NOT NULL,
  db_name         VARCHAR(255)  NOT NULL,
  db_tls          BOOLEAN       NOT NULL DEFAULT FALSE,
  provider        VARCHAR(50)   NOT NULL,
  cluster_id      VARCHAR(255)  NULL,
  claim_url       TEXT          NULL,
  claim_expires_at TIMESTAMP    NULL,
  status          VARCHAR(20)   NOT NULL DEFAULT 'provisioning',
  schema_version  INT           NOT NULL DEFAULT 1,
  created_at      TIMESTAMP     DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP     DEFAULT CURRENT_TIMESTAMP,
  deleted_at      TIMESTAMP     NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_name ON tenants(name);
CREATE INDEX IF NOT EXISTS idx_tenant_status ON tenants(status);
CREATE INDEX IF NOT EXISTS idx_tenant_provider ON tenants(provider);

CREATE TABLE IF NOT EXISTS tenant_tokens (
  api_token     VARCHAR(64)   PRIMARY KEY,
  tenant_id     VARCHAR(36)   NOT NULL,
  created_at    TIMESTAMP     DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_tenant ON tenant_tokens(tenant_id);

-- Upload task tracking (control plane).
CREATE TABLE IF NOT EXISTS upload_tasks (
  task_id       VARCHAR(36)   PRIMARY KEY,
  tenant_id     VARCHAR(36)   NOT NULL,
  file_name     VARCHAR(255)  NOT NULL,
  file_path     TEXT          NOT NULL,
  agent_id      VARCHAR(100)  NULL,
  session_id    VARCHAR(100)  NULL,
  file_type     VARCHAR(20)   NOT NULL,
  total_chunks  INT           NOT NULL DEFAULT 0,
  done_chunks   INT           NOT NULL DEFAULT 0,
  status        VARCHAR(20)   NOT NULL DEFAULT 'pending',
  error_msg     TEXT          NULL,
  created_at    TIMESTAMP     DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP     DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_upload_tenant ON upload_tasks(tenant_id);
CREATE INDEX IF NOT EXISTS idx_upload_poll ON upload_tasks(status, created_at);

-- Tenant data plane schema (per-tenant PostgreSQL database).
-- Run CREATE EXTENSION vector; before this schema if pgvector is installed.

CREATE TABLE IF NOT EXISTS memories (
  id              VARCHAR(36)     PRIMARY KEY,
  content         TEXT            NOT NULL,
  source          VARCHAR(100),
  tags            JSONB,
  metadata        JSONB,
  embedding       vector(1536)    NULL,

  memory_type     VARCHAR(20)     NOT NULL DEFAULT 'pinned',
  agent_id        VARCHAR(100)    NULL,
  session_id      VARCHAR(100)    NULL,
  state           VARCHAR(20)     NOT NULL DEFAULT 'active',
  version         INT             DEFAULT 1,
  updated_by      VARCHAR(100),
  created_at      TIMESTAMP       DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP       DEFAULT CURRENT_TIMESTAMP,
  superseded_by   VARCHAR(36)     NULL
);
CREATE INDEX IF NOT EXISTS idx_memory_type ON memories(memory_type);
CREATE INDEX IF NOT EXISTS idx_source ON memories(source);
CREATE INDEX IF NOT EXISTS idx_state ON memories(state);
CREATE INDEX IF NOT EXISTS idx_agent ON memories(agent_id);
CREATE INDEX IF NOT EXISTS idx_session ON memories(session_id);
CREATE INDEX IF NOT EXISTS idx_updated ON memories(updated_at);

-- pgvector cosine distance index (requires pgvector extension).
-- CREATE INDEX IF NOT EXISTS idx_cosine ON memories USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
-- For small datasets, HNSW index is recommended:
-- CREATE INDEX IF NOT EXISTS idx_cosine ON memories USING hnsw (embedding vector_cosine_ops);

-- GIN index for full-text search.
CREATE INDEX IF NOT EXISTS idx_fts_content ON memories USING gin(to_tsvector('simple', content));
