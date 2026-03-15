package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/qiffang/mnemos/server/internal/domain"
	"github.com/qiffang/mnemos/server/internal/embed"
	"github.com/qiffang/mnemos/server/internal/repository"
)

// IngestRequest is the input for the ingest pipeline.
type IngestRequest struct {
	Messages  []IngestMessage `json:"messages"`
	SessionID string          `json:"session_id"`
	AgentID   string          `json:"agent_id"`
}

// IngestMessage represents a single conversation message.
type IngestMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// IngestResult is the output of the ingest pipeline.
type IngestResult struct {
	Status          string   `json:"status"`           // complete | partial | failed
	MemoriesChanged int      `json:"memories_changed"` // count of ADD + UPDATE actions executed
	InsightIDs      []string `json:"insight_ids,omitempty"`
	Warnings        int      `json:"warnings,omitempty"`
	Error           string   `json:"error,omitempty"`
}

// IngestService handles memory ingestion.
// LLM-based smart pipeline has been moved to the OpenClaw plugin;
// the server always runs in raw mode (direct storage with optional embedding).
type IngestService struct {
	memories  repository.MemoryRepo
	embedder  *embed.Embedder
	autoModel string
}

// NewIngestService creates a new IngestService (raw mode only).
func NewIngestService(
	memories repository.MemoryRepo,
	embedder *embed.Embedder,
	autoModel string,
) *IngestService {
	return &IngestService{
		memories:  memories,
		embedder:  embedder,
		autoModel: autoModel,
	}
}

// Ingest stores conversation messages as raw memory (direct storage with optional embedding).
// Smart processing (fact extraction + reconciliation) is handled by the plugin side.
func (s *IngestService) Ingest(ctx context.Context, agentName string, req IngestRequest) (*IngestResult, error) {
	slog.Info("ingest pipeline started (raw mode)", "agent", agentName, "agent_id", req.AgentID, "session_id", req.SessionID, "messages", len(req.Messages))
	if len(req.Messages) == 0 {
		return nil, &domain.ValidationError{Field: "messages", Message: "required"}
	}
	return s.ingestRaw(ctx, agentName, req)
}

// ingestRaw stores messages as a single raw memory.
func (s *IngestService) ingestRaw(ctx context.Context, agentName string, req IngestRequest) (*IngestResult, error) {
	content := strings.TrimSpace(formatConversation(req.Messages))
	if content == "" {
		return &IngestResult{Status: "complete"}, nil
	}

	// Cap content size to avoid exceeding DB column limits.
	const maxRawContentRunes = 200000
	content = truncateRunes(content, maxRawContentRunes)

	var embedding []float32
	if s.autoModel == "" && s.embedder != nil {
		var err error
		embedding, err = s.embedder.Embed(ctx, content)
		if err != nil {
			return nil, fmt.Errorf("embed for raw ingest: %w", err)
		}
	}

	now := time.Now()
	m := &domain.Memory{
		ID:         uuid.New().String(),
		Content:    content,
		MemoryType: domain.TypeInsight,
		Source:     agentName,
		AgentID:    req.AgentID,
		SessionID:  req.SessionID,
		Embedding:  embedding,
		State:      domain.StateActive,
		Version:    1,
		UpdatedBy:  agentName,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := s.memories.Create(ctx, m); err != nil {
		return nil, fmt.Errorf("create raw memory: %w", err)
	}
	return &IngestResult{
		Status:          "complete",
		MemoriesChanged: 1,
		InsightIDs:      []string{m.ID},
	}, nil
}

// stripInjectedContext removes <relevant-memories>...</relevant-memories> tags from messages.
func stripInjectedContext(messages []IngestMessage) []IngestMessage {
	result := make([]IngestMessage, 0, len(messages))
	for _, msg := range messages {
		cleaned := stripMemoryTags(msg.Content)
		cleaned = strings.TrimSpace(cleaned)
		if cleaned != "" {
			result = append(result, IngestMessage{Role: msg.Role, Content: cleaned})
		}
	}
	return result
}

// stripMemoryTags removes <relevant-memories>...</relevant-memories> from text.
func stripMemoryTags(s string) string {
	for {
		start := strings.Index(s, "<relevant-memories>")
		if start == -1 {
			break
		}
		end := strings.Index(s, "</relevant-memories>")
		if end == -1 {
			// Malformed tag, remove from start to end.
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</relevant-memories>"):]
	}
	return s
}

// formatConversation formats messages into a conversation string for LLM.
func formatConversation(messages []IngestMessage) string {
	var sb strings.Builder
	for _, msg := range messages {
		role := msg.Role
		if r, _ := utf8.DecodeRuneInString(role); r != utf8.RuneError {
			role = strings.ToUpper(string(r)) + role[utf8.RuneLen(r):]
		}
		sb.WriteString(role)
		sb.WriteString(": ")
		sb.WriteString(msg.Content)
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String())
}

// truncateRunes truncates s to at most maxRunes characters (not bytes),
// appending "..." if truncation occurred. Safe for multi-byte UTF-8.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
