package exporter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"engram/internal/config"
	"engram/internal/observability"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// Exporter performs deterministic one-way export of memories to Markdown files and optional Git snapshots.
type Exporter struct {
	pool    *pgxpool.Pool
	cfg     config.ExportConfig
	metrics *observability.Metrics
	log     zerolog.Logger
}

func NewExporter(pool *pgxpool.Pool, cfg config.ExportConfig, metrics *observability.Metrics, log zerolog.Logger) *Exporter {
	return &Exporter{
		pool:    pool,
		cfg:     cfg,
		metrics: metrics,
		log:     log,
	}
}

func (e *Exporter) OutputDir() string {
	return e.cfg.OutputDir
}

type exportRow struct {
	id          uuid.UUID
	tenantID    uuid.UUID
	scope       string
	projectID   *uuid.UUID
	category    string
	title       string
	contentMD   string
	tags        []string
	isPinned    bool
	status      string
	version     int64
	contentHash string
	updatedAt   time.Time
	deletedAt   *time.Time
}

// ExportAll writes all non-deleted memories deterministically under OutputDir and removes stale files.
func (e *Exporter) ExportAll(ctx context.Context) (int, error) {
	baseDir, err := filepath.Abs(e.cfg.OutputDir)
	if err != nil {
		return 0, fmt.Errorf("resolve export output_dir: %w", err)
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir export output_dir: %w", err)
	}

	items, err := e.queryExportRows(ctx, ArchiveFilter{})
	if err != nil {
		return 0, err
	}

	expectedFiles := make(map[string]bool, len(items))
	for _, item := range items {
		targetPath, err := e.targetFilePath(baseDir, item)
		if err != nil {
			return 0, err
		}
		expectedFiles[targetPath] = true

		rendered := RenderMarkdown(item)
		if err := writeFileDeterministic(targetPath, rendered); err != nil {
			return 0, err
		}
	}

	// Clean up stale .md files (soft-deleted memories or changed scopes) and empty directories
	tenantsRoot := filepath.Join(baseDir, "tenants")
	if _, err := os.Stat(tenantsRoot); err == nil {
		var dirs []string
		err = filepath.WalkDir(tenantsRoot, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if path != tenantsRoot {
					dirs = append(dirs, path)
				}
				return nil
			}
			if strings.HasSuffix(d.Name(), ".md") && !expectedFiles[path] {
				if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
					return rmErr
				}
			}
			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("cleanup stale exported markdown files: %w", err)
		}
		// Remove empty directories from deepest to shallowest
		for i := len(dirs) - 1; i >= 0; i-- {
			entries, rdErr := os.ReadDir(dirs[i])
			if rdErr == nil && len(entries) == 0 {
				_ = os.Remove(dirs[i])
			}
		}
	}

	if e.cfg.GitCommit {
		if err := e.commitGitSnapshot(ctx, baseDir); err != nil {
			e.log.Warn().Err(err).Msg("git commit after export failed")
		}
	}

	if e.metrics != nil {
		e.metrics.SetExportBacklog(0)
	}
	return len(items), nil
}

// ExportArchive writes all non-deleted memories as Markdown files into a ZIP archive written to w.
func (e *Exporter) ExportArchive(ctx context.Context, w io.Writer) (int, error) {
	items, err := e.queryExportRows(ctx, ArchiveFilter{})
	if err != nil {
		return 0, err
	}
	if err := writeArchive(w, items, nil); err != nil {
		return 0, err
	}
	return len(items), nil
}

// ArchiveFilter narrows an archive to one tenant, optionally one scope, project, or memory.
// Deleted selects "" for active memories only, "all" to add the recycle bin, or "only" for the recycle bin.
type ArchiveFilter struct {
	TenantID  *uuid.UUID
	Scope     string
	ProjectID *uuid.UUID
	MemoryID  *uuid.UUID
	Deleted   string
}

// ArchivedMemory identifies one memory written into a filtered archive.
type ArchivedMemory struct {
	ID        uuid.UUID  `json:"id"`
	Version   int64      `json:"version"`
	Scope     string     `json:"scope"`
	ProjectID *uuid.UUID `json:"project_id,omitempty"`
	Title     string     `json:"title"`
	Path      string     `json:"path"`
	Deleted   bool       `json:"deleted,omitempty"`
}

// ExportFilteredArchive writes the non-deleted memories matching f as Markdown files plus a
// manifest.json describing the archive, and returns exactly which memory versions it contains.
func (e *Exporter) ExportFilteredArchive(ctx context.Context, w io.Writer, f ArchiveFilter, manifest map[string]any) ([]ArchivedMemory, error) {
	items, err := e.queryExportRows(ctx, f)
	if err != nil {
		return nil, err
	}
	archived := make([]ArchivedMemory, 0, len(items))
	for _, item := range items {
		relPath, err := e.relativeFilePath(item)
		if err != nil {
			return nil, err
		}
		archived = append(archived, ArchivedMemory{ID: item.id, Version: item.version, Scope: item.scope, ProjectID: item.projectID, Title: item.title, Path: filepath.ToSlash(relPath), Deleted: item.deletedAt != nil})
	}
	if manifest == nil {
		manifest = map[string]any{}
	}
	manifest["count"] = len(archived)
	manifest["items"] = archived
	if err := writeArchive(w, items, manifest); err != nil {
		return nil, err
	}
	return archived, nil
}

// writeArchive renders memories into a ZIP stream, adding manifest.json when one is given.
func writeArchive(w io.Writer, items []exportRow, manifest map[string]any) error {
	zw := zip.NewWriter(w)
	if manifest != nil {
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return fmt.Errorf("encode archive manifest: %w", err)
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Deflate, Modified: time.Now().UTC()})
		if err != nil {
			return fmt.Errorf("create manifest entry: %w", err)
		}
		if _, err := fw.Write(append(data, '\n')); err != nil {
			return fmt.Errorf("write manifest entry: %w", err)
		}
	}
	for _, item := range items {
		relPath, err := archivePath(item)
		if err != nil {
			_ = zw.Close()
			return err
		}
		hdr := &zip.FileHeader{
			Name:     filepath.ToSlash(relPath),
			Method:   zip.Deflate,
			Modified: item.updatedAt.UTC(),
		}
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			_ = zw.Close()
			return fmt.Errorf("create zip entry %s: %w", relPath, err)
		}
		if _, err := fw.Write(RenderMarkdown(item)); err != nil {
			_ = zw.Close()
			return fmt.Errorf("write zip entry %s: %w", relPath, err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("finalize zip archive: %w", err)
	}
	return nil
}

func (e *Exporter) queryExportRows(ctx context.Context, f ArchiveFilter) ([]exportRow, error) {
	var where []string
	switch f.Deleted {
	case "all":
		where = append(where, "true")
	case "only":
		where = append(where, "deleted_at IS NOT NULL")
	default:
		where = append(where, "deleted_at IS NULL")
	}
	var args []any
	if f.TenantID != nil {
		args = append(args, *f.TenantID)
		where = append(where, fmt.Sprintf("tenant_id = $%d", len(args)))
	}
	if f.Scope != "" {
		args = append(args, f.Scope)
		where = append(where, fmt.Sprintf("scope = $%d", len(args)))
	}
	if f.ProjectID != nil {
		args = append(args, *f.ProjectID)
		where = append(where, fmt.Sprintf("project_id = $%d", len(args)))
	}
	if f.MemoryID != nil {
		args = append(args, *f.MemoryID)
		where = append(where, fmt.Sprintf("id = $%d", len(args)))
	}
	rows, err := e.pool.Query(ctx, `
		SELECT id, tenant_id, scope, project_id, category, title, content_md,
		       tags, is_pinned, status, version, content_hash, updated_at, deleted_at
		FROM memories
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY tenant_id ASC, scope ASC, project_id ASC NULLS FIRST, id ASC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query memories for export: %w", err)
	}
	defer rows.Close()

	var items []exportRow
	for rows.Next() {
		var r exportRow
		if err := rows.Scan(
			&r.id, &r.tenantID, &r.scope, &r.projectID, &r.category, &r.title, &r.contentMD,
			&r.tags, &r.isPinned, &r.status, &r.version, &r.contentHash, &r.updatedAt, &r.deletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan memory for export: %w", err)
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memories for export: %w", err)
	}
	return items, nil
}

// RunPeriodicLoop runs ExportAll on the configured interval until ctx is cancelled.
func (e *Exporter) RunPeriodicLoop(ctx context.Context) {
	if !e.cfg.Enabled {
		return
	}
	interval := e.cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := e.ExportAll(ctx); err != nil {
				e.log.Error().Err(err).Msg("periodic markdown export failed")
			}
		}
	}
}

func (e *Exporter) relativeFilePath(r exportRow) (string, error) { return archivePath(r) }

// archivePath places global memories under tenants/<tenant>/global and project memories under their project.
func archivePath(r exportRow) (string, error) {
	if r.scope == "global" {
		return filepath.Join("tenants", r.tenantID.String(), "global", r.id.String()+".md"), nil
	}
	if r.projectID == nil {
		return "", fmt.Errorf("project memory %s missing project_id", r.id)
	}
	return filepath.Join("tenants", r.tenantID.String(), "projects", r.projectID.String(), r.id.String()+".md"), nil
}

func (e *Exporter) targetFilePath(baseDir string, r exportRow) (string, error) {
	rel, err := e.relativeFilePath(r)
	if err != nil {
		return "", err
	}
	full := filepath.Clean(filepath.Join(baseDir, rel))
	if !strings.HasPrefix(full, baseDir+string(filepath.Separator)) {
		return "", fmt.Errorf("export path escapes base directory: %s", full)
	}
	return full, nil
}

// RenderMarkdown formats a memory entry with deterministic YAML Front Matter (Section 13).
func RenderMarkdown(r exportRow) []byte {
	var b bytes.Buffer
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", r.id.String())
	fmt.Fprintf(&b, "version: %d\n", r.version)
	fmt.Fprintf(&b, "scope: %s\n", r.scope)
	if r.projectID != nil {
		fmt.Fprintf(&b, "project_id: %s\n", r.projectID.String())
	}
	fmt.Fprintf(&b, "category: %s\n", r.category)
	fmt.Fprintf(&b, "title: %q\n", r.title)
	if len(r.tags) > 0 {
		sortedTags := append([]string(nil), r.tags...)
		sort.Strings(sortedTags)
		b.WriteString("tags:\n")
		for _, t := range sortedTags {
			fmt.Fprintf(&b, "  - %q\n", t)
		}
	} else {
		b.WriteString("tags: []\n")
	}
	fmt.Fprintf(&b, "is_pinned: %t\n", r.isPinned)
	fmt.Fprintf(&b, "status: %s\n", r.status)
	fmt.Fprintf(&b, "content_hash: %s\n", r.contentHash)
	fmt.Fprintf(&b, "updated_at: %s\n", r.updatedAt.UTC().Format(time.RFC3339))
	if r.deletedAt != nil {
		fmt.Fprintf(&b, "deleted_at: %s\n", r.deletedAt.UTC().Format(time.RFC3339))
	}
	b.WriteString("---\n\n")

	body := strings.ReplaceAll(strings.TrimSpace(r.contentMD), "\r\n", "\n")
	b.WriteString(body)
	b.WriteString("\n")
	return b.Bytes()
}

func writeFileDeterministic(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir parent for %s: %w", path, err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, content, 0o644); err != nil {
		return fmt.Errorf("write temp file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp file %s: %w", path, err)
	}
	return nil
}

func (e *Exporter) commitGitSnapshot(ctx context.Context, baseDir string) error {
	gitDir := filepath.Join(baseDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		initCmd := exec.CommandContext(ctx, "git", "init")
		initCmd.Dir = baseDir
		if out, err := initCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %w (%s)", err, string(out))
		}
	}

	addCmd := exec.CommandContext(ctx, "git", "add", "-A")
	addCmd.Dir = baseDir
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w (%s)", err, string(out))
	}

	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = baseDir
	out, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil
	}

	commitCmd := exec.CommandContext(ctx, "git",
		"-c", "user.name=engram-exporter",
		"-c", "user.email=exporter@engram.local",
		"commit", "-m", "chore(export): deterministic snapshot "+time.Now().UTC().Format(time.RFC3339),
	)
	commitCmd.Dir = baseDir
	if cOut, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w (%s)", err, string(cOut))
	}
	return nil
}
