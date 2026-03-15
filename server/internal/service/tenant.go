package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/qiffang/mnemos/server/internal/domain"
	"github.com/qiffang/mnemos/server/internal/repository"
	"github.com/qiffang/mnemos/server/internal/tenant"
)

const tenantMemorySchemaBase = `CREATE TABLE IF NOT EXISTS memories (
	    id              VARCHAR(36)     PRIMARY KEY,
	    content         TEXT            NOT NULL,
	    source          VARCHAR(100),
	    tags            JSON,
	    metadata        JSON,
	    %s
	    memory_type     VARCHAR(20)     NOT NULL DEFAULT 'pinned',
	    agent_id        VARCHAR(100)    NULL,
	    session_id      VARCHAR(100)    NULL,
	    state           VARCHAR(20)     NOT NULL DEFAULT 'active',
	    version         INT             DEFAULT 1,
	    updated_by      VARCHAR(100),
	    superseded_by   VARCHAR(36)     NULL,
	    created_at      TIMESTAMP       DEFAULT CURRENT_TIMESTAMP,
	    updated_at      TIMESTAMP       DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
	    INDEX idx_memory_type         (memory_type),
	    INDEX idx_source              (source),
	    INDEX idx_state               (state),
	    INDEX idx_agent               (agent_id),
	    INDEX idx_session             (session_id),
	    INDEX idx_updated             (updated_at)
	)`

func buildMemorySchema(autoModel string, autoDims int) string {
	var embeddingCol string
	if autoModel != "" {
		dims := strconv.Itoa(autoDims)
		embeddingCol = `embedding VECTOR(` + dims + `) GENERATED ALWAYS AS (EMBED_TEXT('` + autoModel + `', content)) STORED,`
	} else {
		embeddingCol = `embedding VECTOR(1536) NULL,`
	}
	return fmt.Sprintf(tenantMemorySchemaBase, embeddingCol)
}

type TenantService struct {
	tenants    repository.TenantRepo
	zero       *tenant.ZeroClient
	pool       *tenant.TenantPool
	logger     *slog.Logger
	autoModel  string
	autoDims   int
	ftsEnabled bool
	dbDriver   string
}

func NewTenantService(
	tenants repository.TenantRepo,
	zero *tenant.ZeroClient,
	pool *tenant.TenantPool,
	logger *slog.Logger,
	autoModel string,
	autoDims int,
	ftsEnabled bool,
	dbDriver string,
) *TenantService {
	if dbDriver == "" {
		dbDriver = "mysql"
	}
	return &TenantService{
		tenants:    tenants,
		zero:       zero,
		pool:       pool,
		logger:     logger,
		autoModel:  autoModel,
		autoDims:   autoDims,
		ftsEnabled: ftsEnabled,
		dbDriver:   dbDriver,
	}
}

// ProvisionResult is the output of Provision.
type ProvisionResult struct {
	ID string `json:"id"`
}

// Provision creates a new TiDB Zero instance and registers it as a tenant.
// The TiDB Zero instance ID is used as the tenant ID.
func (s *TenantService) Provision(ctx context.Context) (*ProvisionResult, error) {
	if s.zero == nil {
		return nil, &domain.ValidationError{Message: "provisioning disabled (TiDB Zero not configured)"}
	}

	instance, err := s.zero.CreateInstance(ctx, "vmems")
	if err != nil {
		return nil, fmt.Errorf("provision TiDB Zero instance: %w", err)
	}

	// Use the TiDB Zero instance ID as the tenant ID.
	tenantID := instance.ID

	t := &domain.Tenant{
		ID:             tenantID,
		Name:           tenantID, // Use ID as name for auto-provisioned tenants.
		DBHost:         instance.Host,
		DBPort:         instance.Port,
		DBUser:         instance.Username,
		DBPassword:     instance.Password,
		DBName:         "test",
		DBTLS:          true,
		Provider:       "tidb_zero",
		ClusterID:      instance.ID,
		ClaimURL:       instance.ClaimURL,
		ClaimExpiresAt: instance.ClaimExpiresAt,
		Status:         domain.TenantProvisioning,
		SchemaVersion:  0,
	}
	if err := s.tenants.Create(ctx, t); err != nil {
		return nil, fmt.Errorf("create tenant record: %w", err)
	}

	if err := s.initSchema(ctx, t); err != nil {
		if s.logger != nil {
			s.logger.Error("tenant schema init failed", "tenant_id", tenantID, "err", err)
		}
		return nil, fmt.Errorf("init tenant schema: %w", err)
	}

	if err := s.tenants.UpdateStatus(ctx, tenantID, domain.TenantActive); err != nil {
		return nil, fmt.Errorf("activate tenant: %w", err)
	}
	if err := s.tenants.UpdateSchemaVersion(ctx, tenantID, 1); err != nil {
		return nil, fmt.Errorf("update schema version: %w", err)
	}

	return &ProvisionResult{
		ID: tenantID,
	}, nil
}

// GetInfo returns tenant info including agent and memory counts.
func (s *TenantService) GetInfo(ctx context.Context, tenantID string) (*domain.TenantInfo, error) {
	t, err := s.tenants.GetByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	if s.pool == nil {
		return nil, fmt.Errorf("tenant pool not configured")
	}
	db, err := s.pool.Get(ctx, tenantID, t.DSNForDriver(s.dbDriver))
	if err != nil {
		return nil, err
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories").Scan(&count); err != nil {
		return nil, err
	}

	return &domain.TenantInfo{
		TenantID:    t.ID,
		Name:        t.Name,
		Status:      t.Status,
		Provider:    t.Provider,
		MemoryCount: count,
		CreatedAt:   t.CreatedAt,
	}, nil
}

func (s *TenantService) initSchema(ctx context.Context, t *domain.Tenant) error {
	if s.pool == nil {
		return fmt.Errorf("tenant pool not configured")
	}
	dsn := t.DSNForDriver(s.dbDriver)
	db, err := s.pool.Get(ctx, t.ID, dsn)
	if err != nil {
		return err
	}

	switch s.dbDriver {
	case "postgres":
		return s.initSchemaPostgres(ctx, db)
	case "sqlite":
		return s.initSchemaSQLite(ctx, db)
	default:
		return s.initSchemaMySQL(ctx, db)
	}
}

func (s *TenantService) initSchemaMySQL(ctx context.Context, db *sql.DB) error {
	schema := buildMemorySchema(s.autoModel, s.autoDims)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("init tenant schema: memories: %w", err)
	}
	if s.autoModel != "" {
		_, err := db.ExecContext(ctx,
			`ALTER TABLE memories ADD VECTOR INDEX idx_cosine ((VEC_COSINE_DISTANCE(embedding))) ADD_COLUMNAR_REPLICA_ON_DEMAND`)
		if err != nil && !isIndexExistsError(err) {
			return fmt.Errorf("init tenant schema: vector index: %w", err)
		}
	}
	if s.ftsEnabled {
		_, err := db.ExecContext(ctx,
			`ALTER TABLE memories ADD FULLTEXT INDEX idx_fts_content (content) WITH PARSER MULTILINGUAL ADD_COLUMNAR_REPLICA_ON_DEMAND`)
		if err != nil && !isIndexExistsError(err) {
			return fmt.Errorf("init tenant schema: fulltext index: %w", err)
		}
	}
	return nil
}

func (s *TenantService) initSchemaPostgres(ctx context.Context, db *sql.DB) error {
	schema := `CREATE TABLE IF NOT EXISTS memories (
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
	)`
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("init tenant schema: memories: %w", err)
	}
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_memory_type ON memories(memory_type)`,
		`CREATE INDEX IF NOT EXISTS idx_source ON memories(source)`,
		`CREATE INDEX IF NOT EXISTS idx_state ON memories(state)`,
		`CREATE INDEX IF NOT EXISTS idx_agent ON memories(agent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_session ON memories(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_updated ON memories(updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_fts_content ON memories USING gin(to_tsvector('simple', content))`,
	}
	for _, idx := range indexes {
		if _, err := db.ExecContext(ctx, idx); err != nil && !isIndexExistsError(err) {
			s.logger.Warn("pg index creation skipped", "err", err)
		}
	}
	return nil
}

func (s *TenantService) initSchemaSQLite(ctx context.Context, db *sql.DB) error {
	schema := `CREATE TABLE IF NOT EXISTS memories (
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
	)`
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("init tenant schema: memories: %w", err)
	}
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_memory_type ON memories(memory_type)`,
		`CREATE INDEX IF NOT EXISTS idx_source ON memories(source)`,
		`CREATE INDEX IF NOT EXISTS idx_state ON memories(state)`,
		`CREATE INDEX IF NOT EXISTS idx_agent ON memories(agent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_session ON memories(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_updated ON memories(updated_at)`,
	}
	for _, idx := range indexes {
		if _, err := db.ExecContext(ctx, idx); err != nil {
			s.logger.Warn("sqlite index creation skipped", "err", err)
		}
	}
	if s.ftsEnabled {
		_, err := db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(content, id UNINDEXED)`)
		if err != nil {
			s.logger.Warn("sqlite FTS5 table creation skipped", "err", err)
		}
	}
	return nil
}

func isIndexExistsError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1061
	}
	if strings.Contains(err.Error(), "already exists") {
		return true
	}
	return false
}
