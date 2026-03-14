package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/qiffang/mnemos/server/internal/domain"
)

const vectorSearchMaxCandidates = 10000

type MemoryRepo struct {
	db           *sql.DB
	autoModel    string // always empty on SQLite
	ftsAvailable atomic.Bool
}

func NewMemoryRepo(db *sql.DB, autoModel string, ftsEnabled bool) *MemoryRepo {
	r := &MemoryRepo{db: db, autoModel: autoModel}
	r.ftsAvailable.Store(ftsEnabled)
	if ftsEnabled {
		if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(content, id UNINDEXED)`); err != nil {
			slog.Warn("failed to create memories_fts, FTS disabled", "err", err)
			r.ftsAvailable.Store(false)
		} else {
			slog.Info("FTS5 search enabled via MNEMO_FTS_ENABLED")
		}
	}
	return r
}

func (r *MemoryRepo) FTSAvailable() bool { return r.ftsAvailable.Load() }

const allColumns = `id, content, source, tags, metadata, embedding, memory_type, agent_id, session_id, state, version, updated_by, created_at, updated_at, superseded_by`

func (r *MemoryRepo) Create(ctx context.Context, m *domain.Memory) error {
	tagsJSON := marshalTags(m.Tags)
	memoryType := string(m.MemoryType)
	if memoryType == "" {
		memoryType = string(domain.TypePinned)
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO memories (id, content, source, tags, metadata, embedding, memory_type, agent_id, session_id, state, version, updated_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, datetime('now'), datetime('now'))`,
		m.ID, m.Content, nullString(m.Source),
		tagsJSON, nullJSON(m.Metadata), vecToString(m.Embedding), memoryType, nullString(m.AgentID), nullString(m.SessionID),
		m.Version, nullString(m.UpdatedBy),
	)
	if err != nil {
		return fmt.Errorf("create memory: %w", err)
	}
	if r.ftsAvailable.Load() {
		_, _ = r.db.ExecContext(ctx, `INSERT INTO memories_fts (content, id) VALUES (?, ?)`, m.Content, m.ID)
	}
	return nil
}

func (r *MemoryRepo) GetByID(ctx context.Context, id string) (*domain.Memory, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+allColumns+` FROM memories WHERE id = ? AND state = 'active'`, id,
	)
	return scanMemory(row)
}

func (r *MemoryRepo) UpdateOptimistic(ctx context.Context, m *domain.Memory, expectedVersion int) error {
	tagsJSON := marshalTags(m.Tags)

	query := `UPDATE memories SET content = ?, tags = ?, metadata = ?, embedding = ?, version = version + 1, updated_by = ?, updated_at = datetime('now') WHERE id = ?`
	args := []any{m.Content, tagsJSON, nullJSON(m.Metadata), vecToString(m.Embedding), nullString(m.UpdatedBy), m.ID}
	if expectedVersion > 0 {
		query += " AND version = ?"
		args = append(args, expectedVersion)
	}

	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update memory: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	if r.ftsAvailable.Load() {
		_, _ = r.db.ExecContext(ctx, `DELETE FROM memories_fts WHERE id = ?`, m.ID)
		_, _ = r.db.ExecContext(ctx, `INSERT INTO memories_fts (content, id) VALUES (?, ?)`, m.Content, m.ID)
	}
	return nil
}

func (r *MemoryRepo) SoftDelete(ctx context.Context, id, agentName string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("soft delete begin tx: %w", err)
	}
	defer tx.Rollback()

	var state sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT state FROM memories WHERE id = ?`, id).Scan(&state)
	if err == sql.ErrNoRows {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("soft delete lock row: %w", err)
	}

	if state.String == string(domain.StateDeleted) {
		return tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `UPDATE memories SET state = 'deleted', updated_at = datetime('now') WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("soft delete update: %w", err)
	}
	if r.ftsAvailable.Load() {
		_, _ = tx.ExecContext(ctx, `DELETE FROM memories_fts WHERE id = ?`, id)
	}
	return tx.Commit()
}

func (r *MemoryRepo) ArchiveMemory(ctx context.Context, id, supersededBy string) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE memories SET state = 'archived', superseded_by = ?, updated_at = datetime('now')
		 WHERE id = ? AND state = 'active'`,
		supersededBy, id,
	)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *MemoryRepo) ArchiveAndCreate(ctx context.Context, archiveID, supersededBy string, newMem *domain.Memory) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx,
		`UPDATE memories SET state = 'archived', superseded_by = ?, updated_at = datetime('now')
		 WHERE id = ? AND state = 'active'`,
		supersededBy, archiveID,
	)
	if err != nil {
		return fmt.Errorf("archive old memory: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}

	tagsJSON := marshalTags(newMem.Tags)
	memoryType := string(newMem.MemoryType)
	if memoryType == "" {
		memoryType = string(domain.TypePinned)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO memories (id, content, source, tags, metadata, embedding, memory_type, agent_id, session_id, state, version, updated_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, datetime('now'), datetime('now'))`,
		newMem.ID, newMem.Content, nullString(newMem.Source),
		tagsJSON, nullJSON(newMem.Metadata), vecToString(newMem.Embedding), memoryType, nullString(newMem.AgentID), nullString(newMem.SessionID),
		newMem.Version, nullString(newMem.UpdatedBy),
	)
	if err != nil {
		return fmt.Errorf("create new memory: %w", err)
	}
	if r.ftsAvailable.Load() {
		_, _ = tx.ExecContext(ctx, `INSERT INTO memories_fts (content, id) VALUES (?, ?)`, newMem.Content, newMem.ID)
	}
	return tx.Commit()
}

func (r *MemoryRepo) SetState(ctx context.Context, id string, state domain.MemoryState) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE memories SET state = ?, updated_at = datetime('now') WHERE id = ? AND state = 'active'`,
		string(state), id,
	)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *MemoryRepo) List(ctx context.Context, f domain.MemoryFilter) ([]domain.Memory, int, error) {
	where, args := r.buildWhere(f)

	var total int
	countQuery := "SELECT COUNT(*) FROM memories WHERE " + where
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count memories: %w", err)
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	dataQuery := "SELECT " + allColumns + " FROM memories WHERE " + where + " ORDER BY updated_at DESC LIMIT ? OFFSET ?"
	dataArgs := append(args, limit, offset)

	rows, err := r.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()

	var memories []domain.Memory
	for rows.Next() {
		m, err := scanMemoryRows(rows)
		if err != nil {
			return nil, 0, err
		}
		memories = append(memories, *m)
	}
	return memories, total, rows.Err()
}

func (r *MemoryRepo) Count(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories WHERE state = 'active'`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count memories: %w", err)
	}
	return count, nil
}

func (r *MemoryRepo) ListBootstrap(ctx context.Context, limit int) ([]domain.Memory, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+allColumns+` FROM memories WHERE state = 'active' ORDER BY updated_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list bootstrap: %w", err)
	}
	defer rows.Close()

	var memories []domain.Memory
	for rows.Next() {
		m, err := scanMemoryRows(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, *m)
	}
	return memories, rows.Err()
}

func (r *MemoryRepo) BulkCreate(ctx context.Context, memories []*domain.Memory) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmtSQL := `INSERT INTO memories (id, content, source, tags, metadata, embedding, memory_type, agent_id, session_id, state, version, updated_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, datetime('now'), datetime('now'))`
	stmt, err := tx.PrepareContext(ctx, stmtSQL)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	var ftsStmt *sql.Stmt
	if r.ftsAvailable.Load() {
		ftsStmt, err = tx.PrepareContext(ctx, `INSERT INTO memories_fts (content, id) VALUES (?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare fts: %w", err)
		}
		defer ftsStmt.Close()
	}

	for _, m := range memories {
		tagsJSON := marshalTags(m.Tags)
		memoryType := string(m.MemoryType)
		if memoryType == "" {
			memoryType = string(domain.TypePinned)
		}
		_, execErr := stmt.ExecContext(ctx,
			m.ID, m.Content, nullString(m.Source),
			tagsJSON, nullJSON(m.Metadata), vecToString(m.Embedding), memoryType, nullString(m.AgentID), nullString(m.SessionID),
			m.Version, nullString(m.UpdatedBy),
		)
		if execErr != nil {
			if strings.Contains(execErr.Error(), "UNIQUE constraint") {
				return fmt.Errorf("bulk insert memory %s: %w", m.ID, domain.ErrDuplicateKey)
			}
			return fmt.Errorf("bulk insert memory %s: %w", m.ID, execErr)
		}
		if ftsStmt != nil {
			_, _ = ftsStmt.ExecContext(ctx, m.Content, m.ID)
		}
	}
	return tx.Commit()
}

func (r *MemoryRepo) VectorSearch(ctx context.Context, queryVec []float32, f domain.MemoryFilter, limit int) ([]domain.Memory, error) {
	if len(queryVec) == 0 {
		return nil, nil
	}

	conds, args := r.buildFilterConds(f)
	conds = append(conds, "embedding IS NOT NULL AND embedding != ''")
	where := strings.Join(conds, " AND ")

	query := `SELECT ` + allColumns + ` FROM memories WHERE ` + where + ` LIMIT ?`
	args = append(args, vectorSearchMaxCandidates)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	type rowWithEmbed struct {
		m domain.Memory
		embed string
	}
	var candidates []rowWithEmbed
	for rows.Next() {
		m, embedStr, err := scanMemoryRowWithEmbed(rows)
		if err != nil {
			return nil, err
		}
		vec := parseVec(embedStr)
		if len(vec) == 0 {
			continue
		}
		sim := cosineSimilarity(queryVec, vec)
		score := sim
		m.Score = &score
		candidates = append(candidates, rowWithEmbed{m: *m, embed: embedStr})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Sort by score descending and take top limit
	sort.Slice(candidates, func(i, j int) bool {
		si, sj := 0.0, 0.0
		if candidates[i].m.Score != nil {
			si = *candidates[i].m.Score
		}
		if candidates[j].m.Score != nil {
			sj = *candidates[j].m.Score
		}
		return si > sj
	})
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	result := make([]domain.Memory, len(candidates))
	for i, c := range candidates {
		result[i] = c.m
	}
	return result, nil
}

func (r *MemoryRepo) AutoVectorSearch(ctx context.Context, queryText string, f domain.MemoryFilter, limit int) ([]domain.Memory, error) {
	return nil, nil
}

func (r *MemoryRepo) KeywordSearch(ctx context.Context, query string, f domain.MemoryFilter, limit int) ([]domain.Memory, error) {
	conds, args := r.buildFilterConds(f)
	if query != "" {
		conds = append(conds, "content LIKE '%' || ? || '%'")
		args = append(args, query)
	}
	where := strings.Join(conds, " AND ")
	sqlQuery := `SELECT ` + allColumns + ` FROM memories WHERE ` + where + ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()

	var memories []domain.Memory
	for rows.Next() {
		m, err := scanMemoryRows(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, *m)
	}
	return memories, rows.Err()
}

func (r *MemoryRepo) FTSSearch(ctx context.Context, query string, f domain.MemoryFilter, limit int) ([]domain.Memory, error) {
	if !r.ftsAvailable.Load() {
		return nil, nil
	}
	conds, args := r.buildFilterCondsForFTS(f)
	where := strings.Join(conds, " AND ")
	sqlQuery := `SELECT m.id, m.content, m.source, m.tags, m.metadata, m.embedding, m.memory_type, m.agent_id, m.session_id, m.state, m.version, m.updated_by, m.created_at, m.updated_at, m.superseded_by, fts.rank AS fts_rank
		 FROM memories_fts fts
		 JOIN memories m ON m.id = fts.id
		 WHERE fts.content MATCH ? AND ` + where + `
		 ORDER BY fts.rank
		 LIMIT ?`
	fullArgs := make([]any, 0, len(args)+2)
	fullArgs = append(fullArgs, query)
	fullArgs = append(fullArgs, args...)
	fullArgs = append(fullArgs, limit)

	rows, err := r.db.QueryContext(ctx, sqlQuery, fullArgs...)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()

	var memories []domain.Memory
	for rows.Next() {
		m, err := scanMemoryRowsWithFTSRank(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, *m)
	}
	return memories, rows.Err()
}

func (r *MemoryRepo) buildWhere(f domain.MemoryFilter) (string, []any) {
	conds, args := r.buildFilterConds(f)
	if f.Query != "" {
		conds = append(conds, "content LIKE '%' || ? || '%'")
		args = append(args, f.Query)
	}
	return strings.Join(conds, " AND "), args
}

func (r *MemoryRepo) buildFilterConds(f domain.MemoryFilter) ([]string, []any) {
	conds := []string{}
	args := []any{}

	if f.State == "all" {
	} else if f.State != "" {
		conds = append(conds, "state = ?")
		args = append(args, f.State)
	} else {
		conds = append(conds, "state = 'active'")
	}

	if f.MemoryType != "" {
		types := strings.Split(f.MemoryType, ",")
		if len(types) == 1 {
			conds = append(conds, "memory_type = ?")
			args = append(args, strings.TrimSpace(types[0]))
		} else {
			placeholders := make([]string, len(types))
			for i, t := range types {
				placeholders[i] = "?"
				args = append(args, strings.TrimSpace(t))
			}
			conds = append(conds, "memory_type IN ("+strings.Join(placeholders, ",")+")")
		}
	}

	if f.AgentID != "" {
		conds = append(conds, "agent_id = ?")
		args = append(args, f.AgentID)
	}
	if f.SessionID != "" {
		conds = append(conds, "session_id = ?")
		args = append(args, f.SessionID)
	}
	if f.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, f.Source)
	}
	for _, tag := range f.Tags {
		conds = append(conds, "EXISTS (SELECT 1 FROM json_each(tags) WHERE json_each.value = ?)")
		args = append(args, tag)
	}
	if len(conds) == 0 {
		conds = append(conds, "1=1")
	}
	return conds, args
}

func (r *MemoryRepo) buildFilterCondsForFTS(f domain.MemoryFilter) ([]string, []any) {
	conds, args := r.buildFilterConds(f)
	for i, c := range conds {
		if c == "1=1" {
			continue
		}
		conds[i] = strings.ReplaceAll(c, "state ", "m.state ")
		conds[i] = strings.ReplaceAll(conds[i], "memory_type ", "m.memory_type ")
		conds[i] = strings.ReplaceAll(conds[i], "agent_id ", "m.agent_id ")
		conds[i] = strings.ReplaceAll(conds[i], "session_id ", "m.session_id ")
		conds[i] = strings.ReplaceAll(conds[i], "source ", "m.source ")
		conds[i] = strings.ReplaceAll(conds[i], "json_each(tags)", "json_each(m.tags)")
	}
	return conds, args
}

func parseVec(s string) []float32 {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" || len(s) < 2 {
		return nil
	}
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	parts := strings.Split(s, ",")
	vec := make([]float32, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		f, err := strconv.ParseFloat(p, 32)
		if err != nil {
			return nil
		}
		vec = append(vec, float32(f))
	}
	return vec
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func scanMemory(row *sql.Row) (*domain.Memory, error) {
	var m domain.Memory
	var source, memoryType, agentID, sessionID, state, updatedBy, supersededBy sql.NullString
	var tagsJSON, metadataJSON, embeddingStr []byte
	var createdAt, updatedAt interface{}

	err := row.Scan(&m.ID, &m.Content, &source,
		&tagsJSON, &metadataJSON, &embeddingStr, &memoryType, &agentID, &sessionID, &state, &m.Version, &updatedBy,
		&createdAt, &updatedAt, &supersededBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan memory: %w", err)
	}
	m.CreatedAt, m.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	m.Source = source.String
	m.MemoryType = domain.MemoryType(memoryType.String)
	if m.MemoryType == "" {
		m.MemoryType = domain.TypePinned
	}
	m.AgentID = agentID.String
	m.SessionID = sessionID.String
	m.State = domain.MemoryState(state.String)
	if m.State == "" {
		m.State = domain.StateActive
	}
	m.UpdatedBy = updatedBy.String
	m.SupersededBy = supersededBy.String
	m.Tags = unmarshalTags(tagsJSON)
	m.Metadata = unmarshalRawJSON(metadataJSON)
	return &m, nil
}

func scanMemoryRows(rows *sql.Rows) (*domain.Memory, error) {
	var m domain.Memory
	var source, memoryType, agentID, sessionID, state, updatedBy, supersededBy sql.NullString
	var tagsJSON, metadataJSON, embeddingStr []byte
	var createdAt, updatedAt interface{}

	err := rows.Scan(&m.ID, &m.Content, &source,
		&tagsJSON, &metadataJSON, &embeddingStr, &memoryType, &agentID, &sessionID, &state, &m.Version, &updatedBy,
		&createdAt, &updatedAt, &supersededBy)
	if err != nil {
		return nil, fmt.Errorf("scan memory row: %w", err)
	}
	m.CreatedAt, m.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	m.Source = source.String
	m.MemoryType = domain.MemoryType(memoryType.String)
	if m.MemoryType == "" {
		m.MemoryType = domain.TypePinned
	}
	m.AgentID = agentID.String
	m.SessionID = sessionID.String
	m.State = domain.MemoryState(state.String)
	if m.State == "" {
		m.State = domain.StateActive
	}
	m.UpdatedBy = updatedBy.String
	m.SupersededBy = supersededBy.String
	m.Tags = unmarshalTags(tagsJSON)
	m.Metadata = unmarshalRawJSON(metadataJSON)
	return &m, nil
}

func scanMemoryRowWithEmbed(rows *sql.Rows) (*domain.Memory, string, error) {
	var m domain.Memory
	var source, memoryType, agentID, sessionID, state, updatedBy, supersededBy sql.NullString
	var tagsJSON, metadataJSON, embeddingStr []byte
	var createdAt, updatedAt interface{}

	err := rows.Scan(&m.ID, &m.Content, &source,
		&tagsJSON, &metadataJSON, &embeddingStr, &memoryType, &agentID, &sessionID, &state, &m.Version, &updatedBy,
		&createdAt, &updatedAt, &supersededBy)
	if err != nil {
		return nil, "", fmt.Errorf("scan memory row with embed: %w", err)
	}
	m.CreatedAt, m.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	m.Source = source.String
	m.MemoryType = domain.MemoryType(memoryType.String)
	if m.MemoryType == "" {
		m.MemoryType = domain.TypePinned
	}
	m.AgentID = agentID.String
	m.SessionID = sessionID.String
	m.State = domain.MemoryState(state.String)
	if m.State == "" {
		m.State = domain.StateActive
	}
	m.UpdatedBy = updatedBy.String
	m.SupersededBy = supersededBy.String
	m.Tags = unmarshalTags(tagsJSON)
	m.Metadata = unmarshalRawJSON(metadataJSON)
	embedStr := ""
	if len(embeddingStr) > 0 {
		embedStr = string(embeddingStr)
	}
	return &m, embedStr, nil
}

func scanMemoryRowsWithFTSRank(rows *sql.Rows) (*domain.Memory, error) {
	var m domain.Memory
	var source, memoryType, agentID, sessionID, state, updatedBy, supersededBy sql.NullString
	var tagsJSON, metadataJSON, embeddingStr []byte
	var createdAt, updatedAt interface{}
	var ftsRank float64

	err := rows.Scan(&m.ID, &m.Content, &source,
		&tagsJSON, &metadataJSON, &embeddingStr, &memoryType, &agentID, &sessionID, &state, &m.Version, &updatedBy,
		&createdAt, &updatedAt, &supersededBy,
		&ftsRank)
	if err != nil {
		return nil, fmt.Errorf("scan memory row with fts rank: %w", err)
	}
	m.CreatedAt, m.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	m.Source = source.String
	m.MemoryType = domain.MemoryType(memoryType.String)
	if m.MemoryType == "" {
		m.MemoryType = domain.TypePinned
	}
	m.AgentID = agentID.String
	m.SessionID = sessionID.String
	m.State = domain.MemoryState(state.String)
	if m.State == "" {
		m.State = domain.StateActive
	}
	m.UpdatedBy = updatedBy.String
	m.SupersededBy = supersededBy.String
	m.Tags = unmarshalTags(tagsJSON)
	m.Metadata = unmarshalRawJSON(metadataJSON)
	// FTS5 rank is negative (smaller = better); score = -rank for consistency
	score := -ftsRank
	m.Score = &score
	return &m, nil
}

func parseTime(v interface{}) time.Time {
	if v == nil {
		return time.Time{}
	}
	switch t := v.(type) {
	case time.Time:
		return t
	case []byte:
		parsed, _ := time.Parse("2006-01-02 15:04:05", string(t))
		return parsed
	case string:
		parsed, _ := time.Parse("2006-01-02 15:04:05", t)
		return parsed
	default:
		return time.Time{}
	}
}

func marshalTags(tags []string) []byte {
	if len(tags) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return []byte("[]")
	}
	return b
}

func unmarshalTags(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var tags []string
	if err := json.Unmarshal(data, &tags); err != nil {
		return nil
	}
	return tags
}

func unmarshalRawJSON(data []byte) json.RawMessage {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	return json.RawMessage(data)
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullJSON(data json.RawMessage) any {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	return []byte(data)
}

func vecToString(embedding []float32) any {
	if len(embedding) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, v := range embedding {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(fmt.Sprintf("%g", v))
	}
	sb.WriteByte(']')
	return sb.String()
}
