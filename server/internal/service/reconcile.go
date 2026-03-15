package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/qiffang/mnemos/server/internal/domain"
)

const (
	perFactSearchLimit   = 5
	gatherContentMaxLen  = 150
	maxExistingMemories  = 60
	maxGatherConcurrency = 10
)

// GatherRequest is the input for the gather endpoint.
type GatherRequest struct {
	Facts []string `json:"facts"`
}

// GatherResult is the output of the gather endpoint.
type GatherResult struct {
	Existing []domain.Memory `json:"existing"`
}

// ReconcileAction is a single reconcile event from the plugin's LLM.
type ReconcileAction struct {
	ID        string   `json:"id"`
	Text      string   `json:"text"`
	Event     string   `json:"event"`     // ADD, UPDATE, DELETE, NOOP
	OldMemory string   `json:"old_memory,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

// ExecuteRequest is the input for the execute endpoint.
type ExecuteRequest struct {
	Events     []ReconcileAction `json:"events"`
	ExistingIDs []string         `json:"existing_ids"`
}

// ExecuteResult is the output of the execute endpoint.
type ExecuteResult struct {
	MemoriesChanged int      `json:"memories_changed"`
	CreatedIDs      []string `json:"created_ids,omitempty"`
	Warnings        int      `json:"warnings"`
}

// Gather searches existing memories relevant to the given facts using parallel goroutines.
// Each fact triggers a search (up to maxGatherConcurrency in parallel), results are
// deduplicated, truncated to gatherContentMaxLen, and capped at maxExistingMemories.
func (s *MemoryService) Gather(ctx context.Context, req GatherRequest) (*GatherResult, error) {
	if len(req.Facts) == 0 {
		return &GatherResult{Existing: []domain.Memory{}}, nil
	}

	type searchResult struct {
		memories []domain.Memory
		err      error
	}

	resultsCh := make(chan searchResult, len(req.Facts))
	sem := make(chan struct{}, maxGatherConcurrency)

	var wg sync.WaitGroup
	for _, fact := range req.Facts {
		wg.Add(1)
		go func(query string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			filter := domain.MemoryFilter{
				Query: query,
				Limit: perFactSearchLimit,
			}
			mems, _, err := s.Search(ctx, filter)
			resultsCh <- searchResult{memories: mems, err: err}
		}(fact)
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	seen := make(map[string]struct{})
	var existing []domain.Memory

	for sr := range resultsCh {
		if sr.err != nil {
			slog.Warn("gather: per-fact search failed", "err", sr.err)
			continue
		}
		for _, m := range sr.memories {
			if _, dup := seen[m.ID]; dup {
				continue
			}
			seen[m.ID] = struct{}{}

			content := m.Content
			if len([]rune(content)) > gatherContentMaxLen {
				content = string([]rune(content)[:gatherContentMaxLen])
			}
			m.Content = content
			existing = append(existing, m)
		}
	}

	if len(existing) > maxExistingMemories {
		existing = existing[:maxExistingMemories]
	}
	if existing == nil {
		existing = []domain.Memory{}
	}

	return &GatherResult{Existing: existing}, nil
}

// Execute applies reconcile events using parallel goroutines.
// ADD items are batch-created, UPDATE/DELETE run concurrently.
func (s *MemoryService) Execute(ctx context.Context, agentName string, req ExecuteRequest) (*ExecuteResult, error) {
	existingMap := make(map[string]string, len(req.ExistingIDs))
	for i, id := range req.ExistingIDs {
		existingMap[fmt.Sprintf("%d", i)] = id
	}

	var (
		mu              sync.Mutex
		memoriesChanged int
		warnings        int
		createdIDs      []string
		toAdd           []BulkMemoryInput
	)

	var wg sync.WaitGroup
	sem := make(chan struct{}, maxGatherConcurrency)

	for _, ev := range req.Events {
		event := ev.Event
		switch event {
		case "ADD", "add":
			if ev.Text != "" {
				mu.Lock()
				toAdd = append(toAdd, BulkMemoryInput{
					Content:    ev.Text,
					Tags:       ev.Tags,
					MemoryType: "insight",
				})
				mu.Unlock()
			}
		case "UPDATE", "update":
			if ev.Text == "" {
				continue
			}
			realID, ok := existingMap[ev.ID]
			if !ok {
				continue
			}

			wg.Add(1)
			go func(id, content string, tags []string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				updated, err := s.Update(ctx, agentName, id, content, tags, nil, 0)
				mu.Lock()
				defer mu.Unlock()
				if err != nil || updated == nil {
					slog.Warn("execute: update failed", "id", id, "err", err)
					warnings++
				} else {
					memoriesChanged++
				}
			}(realID, ev.Text, ev.Tags)
		case "DELETE", "delete":
			realID, ok := existingMap[ev.ID]
			if !ok {
				continue
			}

			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				err := s.Delete(ctx, id, agentName)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					slog.Warn("execute: delete failed", "id", id, "err", err)
					warnings++
				} else {
					memoriesChanged++
				}
			}(realID)
		}
	}

	wg.Wait()

	if len(toAdd) > 0 {
		created, err := s.BulkCreate(ctx, agentName, toAdd)
		if err != nil {
			slog.Warn("execute: bulk create failed", "err", err)
			warnings += len(toAdd)
		} else {
			memoriesChanged += len(created)
			for _, m := range created {
				createdIDs = append(createdIDs, m.ID)
			}
		}
	}

	return &ExecuteResult{
		MemoriesChanged: memoriesChanged,
		CreatedIDs:      createdIDs,
		Warnings:        warnings,
	}, nil
}

// TagSearch searches memories by tags only (no text query), used for tag-aware recall.
func (s *MemoryService) TagSearch(ctx context.Context, tags []string, limit int) ([]domain.Memory, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	filter := domain.MemoryFilter{
		Tags:  tags,
		Limit: limit,
	}
	results, _, err := s.memories.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// GatherForTags extracts common tags from existing memories, for tag-aware search boost.
func (s *MemoryService) GatherForTags(ctx context.Context, query string, limit int) ([]domain.Memory, error) {
	if limit <= 0 {
		limit = 10
	}
	filter := domain.MemoryFilter{
		Query: query,
		Limit: limit,
	}
	results, _, err := s.Search(ctx, filter)
	if err != nil {
		return nil, err
	}

	if len(results) == 0 {
		return results, nil
	}

	tagFreq := make(map[string]int)
	for _, m := range results {
		for _, t := range m.Tags {
			tagFreq[t]++
		}
	}

	if len(tagFreq) == 0 {
		return results, nil
	}

	// Find top tags (appearing in 2+ results)
	var topTags []string
	for tag, count := range tagFreq {
		if count >= 2 {
			topTags = append(topTags, tag)
		}
	}
	if len(topTags) == 0 {
		return results, nil
	}

	tagResults, err := s.TagSearch(ctx, topTags, limit)
	if err != nil {
		return results, nil
	}

	seen := make(map[string]struct{}, len(results))
	for _, m := range results {
		seen[m.ID] = struct{}{}
	}

	for _, m := range tagResults {
		if _, dup := seen[m.ID]; dup {
			continue
		}
		seen[m.ID] = struct{}{}
		results = append(results, m)
		if len(results) >= limit {
			break
		}
	}

	return results, nil
}
