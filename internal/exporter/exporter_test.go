package exporter

import (
	"strings"
	"testing"
	"time"

	"engram/internal/config"
	"engram/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func TestRelativeAndTargetFilePath(t *testing.T) {
	exp := NewExporter(nil, config.ExportConfig{OutputDir: "./export"}, observability.NewMetrics(), zerolog.Nop())
	tid := uuid.New()
	pid := uuid.New()
	mid := uuid.New()

	globalPath, err := exp.targetFilePath("/tmp/engram-export", exportRow{
		id:       mid,
		tenantID: tid,
		scope:    "global",
	})
	if err != nil {
		t.Fatalf("targetFilePath global failed: %v", err)
	}
	wantGlobalSuffix := "/tenants/" + tid.String() + "/global/" + mid.String() + ".md"
	if !strings.HasSuffix(globalPath, wantGlobalSuffix) {
		t.Fatalf("unexpected global path %q, want suffix %q", globalPath, wantGlobalSuffix)
	}

	projPath, err := exp.targetFilePath("/tmp/engram-export", exportRow{
		id:        mid,
		tenantID:  tid,
		scope:     "project",
		projectID: &pid,
	})
	if err != nil {
		t.Fatalf("targetFilePath project failed: %v", err)
	}
	wantProjSuffix := "/tenants/" + tid.String() + "/projects/" + pid.String() + "/" + mid.String() + ".md"
	if !strings.HasSuffix(projPath, wantProjSuffix) {
		t.Fatalf("unexpected project path %q, want suffix %q", projPath, wantProjSuffix)
	}
}

func TestRenderMarkdown_DeterministicFrontmatter(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	out := string(RenderMarkdown(exportRow{
		id:          uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		tenantID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		scope:       "global",
		category:    "architecture",
		title:       "Test Doc",
		contentMD:   "Hello world",
		tags:        []string{"z", "a"},
		isPinned:    true,
		status:      "active",
		version:     3,
		contentHash: "abc123",
		updatedAt:   now,
	}))
	if !strings.Contains(out, "  - \"a\"\n  - \"z\"\n") {
		t.Fatalf("expected sorted tags in frontmatter, got:\n%s", out)
	}
	if !strings.HasSuffix(out, "\n---\n\nHello world\n") {
		t.Fatalf("expected markdown body after frontmatter, got:\n%s", out)
	}
}
