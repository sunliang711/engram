package mcp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"engram/internal/auth"
	"engram/internal/memory"
	"engram/internal/project"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func TestToolDefinitions_MemoryDiscoverHasClearDescriptions(t *testing.T) {
	defs := toolDefinitions()
	var discover, query map[string]any
	for _, d := range defs {
		switch d["name"] {
		case "memory_discover":
			discover = d
		case "memory_query":
			query = d
		}
	}
	if discover == nil {
		t.Fatal("memory_discover tool definition not found")
	}
	if query == nil {
		t.Fatal("memory_query tool definition not found")
	}
	discoverDesc, _ := discover["description"].(string)
	for _, want := range []string{"global_index", "project_index", "total_count", "truncated", "memory_query"} {
		if !strings.Contains(discoverDesc, want) {
			t.Fatalf("expected memory_discover description to mention %q, got %q", want, discoverDesc)
		}
	}
	queryDesc, _ := query["description"].(string)
	for _, want := range []string{"limit", "100", "next_cursor"} {
		if !strings.Contains(queryDesc, want) {
			t.Fatalf("expected memory_query description to mention %q, got %q", want, queryDesc)
		}
	}

	schema, ok := discover["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected inputSchema type: %T", discover["inputSchema"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected properties type: %T", schema["properties"])
	}

	for _, field := range []string{"project_marker", "marker_path", "workspace_path", "allow_global_only", "remotes"} {
		prop, ok := props[field].(map[string]any)
		if !ok {
			t.Fatalf("missing property %q in memory_discover", field)
		}
		desc, _ := prop["description"].(string)
		if desc == "" {
			t.Fatalf("expected non-empty description for memory_discover.%s", field)
		}
	}
}

func TestDispatchToolCall_OmitsDuplicateStructuredContent(t *testing.T) {
	srv := &Server{
		log: zerolog.New(io.Discard),
	}
	principal := auth.Principal{
		TenantID:    uuid.New(),
		PrincipalID: uuid.New(),
	}

	res := srv.dispatchToolCall(context.Background(), principal, "unknown_tool", json.RawMessage(`{}`), "req_test")
	if !res.IsError {
		t.Fatal("expected IsError=true for unknown tool")
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" || res.Content[0].Text == "" {
		t.Fatalf("expected single text content item, got %+v", res.Content)
	}

	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal toolCallResult: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal toolCallResult: %v", err)
	}
	if _, exists := m["structuredContent"]; exists {
		t.Fatalf("expected structuredContent to be omitted, got %s", string(raw))
	}
}

func TestCompactIndexAndSummarySerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	resp := memory.DiscoverResponse{
		ProjectContext: "ctx_token",
		Mode:           "project",
		Project: &project.ResolvedProject{
			ID:        uuid.New(),
			TenantID:  uuid.New(),
			Name:      "broker-core",
			MatchedBy: "remote",
		},
		GlobalIndex: memory.ScopeIndex{
			Pinned: []memory.IndexItem{
				{ID: uuid.New(), Category: "config", Title: "Global Rule", Summary: "Always verify before merge", UpdatedAt: now},
			},
			CategoryStats: map[string]int{"config": 1},
			Recent:        []memory.IndexItem{},
			TotalCount:    1,
			Truncated:     false,
		},
		LatestCursor: "cur_1",
	}

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal DiscoverResponse: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal DiscoverResponse: %v", err)
	}
	gi := raw["global_index"].(map[string]any)
	if gi["total_count"] != float64(1) {
		t.Fatalf("expected total_count=1 in global_index, got %v", gi["total_count"])
	}
	if truncatedVal, hasTruncated := gi["truncated"]; !hasTruncated || truncatedVal != false {
		t.Fatalf("expected explicit truncated=false in global_index, got has=%v val=%v", hasTruncated, truncatedVal)
	}
	pinned := gi["pinned"].([]any)[0].(map[string]any)
	if pinned["summary"] != "Always verify before merge" {
		t.Fatalf("expected compact summary in global_index.pinned[0], got %v", pinned)
	}
	for _, forbidden := range []string{"content_hash", "project_id", "source_memory_id", "tags", "status", "scope", "is_pinned"} {
		if _, exists := pinned[forbidden]; exists {
			t.Fatalf("unexpected bloated field %q in discover index item: %v", forbidden, pinned)
		}
	}

	qItem := memory.MemorySummary{
		ID:        uuid.New(),
		Scope:     "project",
		Category:  "env",
		Title:     "UAT URL",
		Summary:   "https://uat.example.com",
		Status:    "active",
		Version:   2,
		UpdatedAt: now,
	}
	qb, err := json.Marshal(qItem)
	if err != nil {
		t.Fatalf("marshal MemorySummary: %v", err)
	}
	var qRaw map[string]any
	if err := json.Unmarshal(qb, &qRaw); err != nil {
		t.Fatalf("unmarshal MemorySummary: %v", err)
	}
	for _, forbidden := range []string{"content_hash", "project_id", "source_memory_id", "tags", "is_pinned"} {
		if _, exists := qRaw[forbidden]; exists {
			t.Fatalf("unexpected bloated field %q in query summary item: %v", forbidden, qRaw)
		}
	}

	projResp := memory.DiscoverResponse{
		ProjectContext: "ctx_token",
		Mode:           "project",
		GlobalIndex: memory.ScopeIndex{
			Pinned:        []memory.IndexItem{},
			CategoryStats: map[string]int{},
			Recent:        []memory.IndexItem{},
		},
		ProjectIndex: &memory.ScopeIndex{
			Pinned:        []memory.IndexItem{},
			CategoryStats: map[string]int{"architecture": 5, "configuration": 2},
			Recent: []memory.IndexItem{
				{
					ID:        uuid.New(),
					Category:  "configuration",
					Title:     "Runtime Config",
					Summary:   "Server limits and defaults",
					Tags:      []string{"config"},
					UpdatedAt: now,
				},
			},
			TotalCount: 7,
			Truncated:  true,
		},
		LatestCursor: "cur_2",
	}
	pb, err := json.Marshal(projResp)
	if err != nil {
		t.Fatalf("marshal DiscoverResponse with ProjectIndex: %v", err)
	}
	var pRaw map[string]any
	if err := json.Unmarshal(pb, &pRaw); err != nil {
		t.Fatalf("unmarshal DiscoverResponse with ProjectIndex: %v", err)
	}
	pi := pRaw["project_index"].(map[string]any)
	if pi["total_count"] != float64(7) || pi["truncated"] != true {
		t.Fatalf("expected total_count=7 and truncated=true in project_index, got %v", pi)
	}
	recent0 := pi["recent"].([]any)[0].(map[string]any)
	if recent0["summary"] != "Server limits and defaults" {
		t.Fatalf("expected compact summary in project_index.recent[0], got %v", recent0)
	}
	for _, forbidden := range []string{"content_hash", "project_id", "source_memory_id", "status", "scope", "is_pinned"} {
		if _, exists := recent0[forbidden]; exists {
			t.Fatalf("unexpected bloated field %q in project index item: %v", forbidden, recent0)
		}
	}
}
