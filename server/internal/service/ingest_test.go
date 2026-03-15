package service

import (
	"context"
	"errors"
	"testing"

	"github.com/qiffang/mnemos/server/internal/domain"
)

type memoryRepoMock struct {
	createCalls          []*domain.Memory
	getByID              map[string]*domain.Memory
	getByIDErr           error
	setStateCalls        []setStateCall  // track SetState invocations
	setStateErr          error           // configurable return value for SetState
	vectorResults        []domain.Memory // configurable results for AutoVectorSearch
	vectorErr            error           // configurable error for AutoVectorSearch / VectorSearch
	ftsResults           []domain.Memory // configurable results for FTSSearch
	ftsErr               error           // configurable error for FTSSearch
	kwResults            []domain.Memory // configurable results for KeywordSearch
	kwErr                error           // configurable error for KeywordSearch
	ftsAvail             bool            // configurable FTSAvailable() return
	lastVectorFilter     domain.MemoryFilter
	lastAutoVectorFilter domain.MemoryFilter
	lastKeywordFilter    domain.MemoryFilter
	lastFTSFilter        domain.MemoryFilter
}

type setStateCall struct {
	ID    string
	State domain.MemoryState
}

func (m *memoryRepoMock) Create(ctx context.Context, mem *domain.Memory) error {
	m.createCalls = append(m.createCalls, mem)
	return nil
}

func (m *memoryRepoMock) GetByID(ctx context.Context, id string) (*domain.Memory, error) {
	if m.getByIDErr != nil {
		return nil, m.getByIDErr
	}
	if mem, ok := m.getByID[id]; ok {
		cp := *mem
		return &cp, nil
	}
	for _, mem := range m.createCalls {
		if mem.ID == id {
			cp := *mem
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (m *memoryRepoMock) UpdateOptimistic(ctx context.Context, mem *domain.Memory, expectedVersion int) error {
	return nil
}

func (m *memoryRepoMock) SoftDelete(ctx context.Context, id, agentName string) error {
	return nil
}

func (m *memoryRepoMock) ArchiveMemory(ctx context.Context, id, supersededBy string) error {
	return nil
}
func (m *memoryRepoMock) ArchiveAndCreate(ctx context.Context, archiveID, supersededBy string, newMem *domain.Memory) error {
	m.createCalls = append(m.createCalls, newMem)
	return nil
}

func (m *memoryRepoMock) SetState(ctx context.Context, id string, state domain.MemoryState) error {
	m.setStateCalls = append(m.setStateCalls, setStateCall{ID: id, State: state})
	return m.setStateErr
}

func (m *memoryRepoMock) List(ctx context.Context, f domain.MemoryFilter) ([]domain.Memory, int, error) {
	return nil, 0, nil
}

func (m *memoryRepoMock) Count(ctx context.Context) (int, error) {
	return 0, nil
}

func (m *memoryRepoMock) BulkCreate(ctx context.Context, memories []*domain.Memory) error {
	return nil
}

func (m *memoryRepoMock) VectorSearch(ctx context.Context, queryVec []float32, f domain.MemoryFilter, limit int) ([]domain.Memory, error) {
	m.lastVectorFilter = f
	return nil, nil
}

func (m *memoryRepoMock) AutoVectorSearch(ctx context.Context, queryText string, f domain.MemoryFilter, limit int) ([]domain.Memory, error) {
	m.lastAutoVectorFilter = f
	if m.vectorErr != nil {
		return nil, m.vectorErr
	}
	if m.vectorResults != nil {
		return m.vectorResults, nil
	}
	return nil, nil
}

func (m *memoryRepoMock) KeywordSearch(ctx context.Context, query string, f domain.MemoryFilter, limit int) ([]domain.Memory, error) {
	m.lastKeywordFilter = f
	if m.kwErr != nil {
		return nil, m.kwErr
	}
	if m.kwResults != nil {
		return m.kwResults, nil
	}
	return nil, nil
}

func (m *memoryRepoMock) FTSSearch(ctx context.Context, query string, f domain.MemoryFilter, limit int) ([]domain.Memory, error) {
	m.lastFTSFilter = f
	if m.ftsErr != nil {
		return nil, m.ftsErr
	}
	if m.ftsResults != nil {
		return m.ftsResults, nil
	}
	return nil, nil
}

func (m *memoryRepoMock) FTSAvailable() bool { return m.ftsAvail }

func (m *memoryRepoMock) ListBootstrap(ctx context.Context, limit int) ([]domain.Memory, error) {
	return nil, nil
}

func TestStripInjectedContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []IngestMessage
		expected []IngestMessage
	}{
		{
			name: "removes relevant memories tag",
			input: []IngestMessage{{
				Role:    "user",
				Content: "keep <relevant-memories>remove</relevant-memories> text",
			}},
			expected: []IngestMessage{{Role: "user", Content: "keep  text"}},
		},
		{
			name: "handles no tags",
			input: []IngestMessage{{
				Role:    "assistant",
				Content: "no tags here",
			}},
			expected: []IngestMessage{{Role: "assistant", Content: "no tags here"}},
		},
		{
			name: "handles malformed tag",
			input: []IngestMessage{{
				Role:    "user",
				Content: "keep <relevant-memories>broken",
			}},
			expected: []IngestMessage{{Role: "user", Content: "keep"}},
		},
		{
			name: "drops empty content",
			input: []IngestMessage{{
				Role:    "system",
				Content: "<relevant-memories>only</relevant-memories>",
			}},
			expected: []IngestMessage{},
		},
		{
			name: "handles multiple tags",
			input: []IngestMessage{{
				Role:    "user",
				Content: "a<relevant-memories>x</relevant-memories>b<relevant-memories>y</relevant-memories>c",
			}},
			expected: []IngestMessage{{Role: "user", Content: "abc"}},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stripInjectedContext(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("stripInjectedContext() len = %d, expected %d; got %#v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Fatalf("stripInjectedContext()[%d] = %#v, expected %#v", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestStripMemoryTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single tag",
			input:    "a<relevant-memories>b</relevant-memories>c",
			expected: "ac",
		},
		{
			name:     "multiple tags",
			input:    "a<relevant-memories>b</relevant-memories>c<relevant-memories>d</relevant-memories>e",
			expected: "ace",
		},
		{
			name:     "malformed tag",
			input:    "prefix<relevant-memories>broken",
			expected: "prefix",
		},
		{
			name:     "nested tags",
			input:    "a<relevant-memories>one<relevant-memories>two</relevant-memories>three</relevant-memories>b",
			expected: "athree</relevant-memories>b",
		},
		{
			name:     "no tags",
			input:    "plain text",
			expected: "plain text",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stripMemoryTags(tt.input)
			if got != tt.expected {
				t.Fatalf("stripMemoryTags() = %q, expected %q", got, tt.expected)
			}
		})
	}
}

func TestFormatConversation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []IngestMessage
		expected string
	}{
		{
			name: "formats role content pairs",
			input: []IngestMessage{{
				Role:    "user",
				Content: "hi",
			}, {
				Role:    "assistant",
				Content: "hello",
			}},
			expected: "User: hi\n\nAssistant: hello",
		},
		{
			name:     "handles empty messages",
			input:    nil,
			expected: "",
		},
		{
			name: "capitalizes first letter only",
			input: []IngestMessage{{
				Role:    "uSER",
				Content: "case",
			}},
			expected: "USER: case",
		},
		{
			name: "trims trailing whitespace",
			input: []IngestMessage{{
				Role:    "user",
				Content: "trail",
			}},
			expected: "User: trail",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatConversation(tt.input)
			if got != tt.expected {
				t.Fatalf("formatConversation() = %q, expected %q", got, tt.expected)
			}
		})
	}
}

func TestIngestEmptyMessages(t *testing.T) {
	t.Parallel()

	svc := NewIngestService(&memoryRepoMock{}, nil, "")
	_, err := svc.Ingest(context.Background(), "agent-1", IngestRequest{})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	var vErr *domain.ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if vErr.Field != "messages" {
		t.Fatalf("expected field 'messages', got %q", vErr.Field)
	}
}

func TestIngestModeRawStoresInsight(t *testing.T) {
	t.Parallel()

	memRepo := &memoryRepoMock{}
	svc := NewIngestService(memRepo, nil, "")

	req := IngestRequest{
		SessionID: "session-1",
		AgentID:   "agent-1",
		Messages: []IngestMessage{{
			Role:    "user",
			Content: "hello",
		}, {
			Role:    "assistant",
			Content: "world",
		}},
	}

	res, err := svc.Ingest(context.Background(), "agent-1", req)
	if err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	if res == nil || res.MemoriesChanged != 1 {
		t.Fatalf("expected 1 insight added, got %#v", res)
	}
	if len(memRepo.createCalls) != 1 {
		t.Fatalf("expected 1 Create call, got %d", len(memRepo.createCalls))
	}

	created := memRepo.createCalls[0]
	expectedContent := "User: hello\n\nAssistant: world"
	if created.Content != expectedContent {
		t.Fatalf("unexpected content: %q", created.Content)
	}
	if created.MemoryType != domain.TypeInsight {
		t.Fatalf("expected memory type insight, got %q", created.MemoryType)
	}
}

func TestIngestAlwaysRaw(t *testing.T) {
	t.Parallel()

	memRepo := &memoryRepoMock{}
	svc := NewIngestService(memRepo, nil, "")

	req := IngestRequest{
		SessionID: "session-2",
		AgentID:   "agent-2",
		Messages: []IngestMessage{{
			Role:    "user",
			Content: "hello",
		}},
	}

	res, err := svc.Ingest(context.Background(), "agent-2", req)
	if err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	if res == nil || res.MemoriesChanged != 1 {
		t.Fatalf("expected 1 insight added, got %#v", res)
	}
	if len(memRepo.createCalls) != 1 {
		t.Fatalf("expected 1 Create call, got %d", len(memRepo.createCalls))
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{name: "short ASCII", input: "hello", max: 10, expected: "hello"},
		{name: "exact ASCII", input: "hello", max: 5, expected: "hello"},
		{name: "truncate ASCII", input: "hello world", max: 5, expected: "hello..."},
		{name: "multibyte no truncate", input: "caf\u00e9", max: 4, expected: "caf\u00e9"},
		{name: "multibyte truncate", input: "caf\u00e9 latt\u00e9", max: 4, expected: "caf\u00e9..."},
		{name: "emoji content", input: "hello\U0001F600world", max: 7, expected: "hello\U0001F600w..."},
		{name: "empty string", input: "", max: 5, expected: ""},
		{name: "zero max", input: "hello", max: 0, expected: "..."},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateRunes(tt.input, tt.max)
			if got != tt.expected {
				t.Fatalf("truncateRunes(%q, %d) = %q, expected %q", tt.input, tt.max, got, tt.expected)
			}
		})
	}
}
