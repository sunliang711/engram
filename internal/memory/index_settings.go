package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"engram/internal/apperror"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	DefaultDiscoverPinnedLimit        = 10
	DefaultDiscoverGlobalRecentLimit  = 5
	DefaultDiscoverProjectRecentLimit = 50
	DefaultIndexSummaryMaxRunes       = 100

	MinDiscoverLimit       = 1
	MaxDiscoverPinnedLimit = 100
	MaxDiscoverGlobalLimit = 200
	MaxDiscoverProjLimit   = 500
	MinIndexSummaryRunes   = 0
	MaxIndexSummaryRunes   = 500
)

// IndexSettings configures the scope index bounds returned by memory_discover.
type IndexSettings struct {
	DiscoverPinnedLimit        int        `json:"discover_pinned_limit"`
	DiscoverGlobalRecentLimit  int        `json:"discover_global_recent_limit"`
	DiscoverProjectRecentLimit int        `json:"discover_project_recent_limit"`
	IndexSummaryMaxRunes       int        `json:"index_summary_max_runes"`
	UpdatedAt                  *time.Time `json:"updated_at,omitempty"`
}

// UpdateIndexSettingsRequest updates one or more persistent discovery index settings.
type UpdateIndexSettingsRequest struct {
	DiscoverPinnedLimit        *int `json:"discover_pinned_limit"`
	DiscoverGlobalRecentLimit  *int `json:"discover_global_recent_limit"`
	DiscoverProjectRecentLimit *int `json:"discover_project_recent_limit"`
	IndexSummaryMaxRunes       *int `json:"index_summary_max_runes"`
}

// DefaultIndexSettings returns the built-in discovery index defaults.
func DefaultIndexSettings() IndexSettings {
	return IndexSettings{
		DiscoverPinnedLimit:        DefaultDiscoverPinnedLimit,
		DiscoverGlobalRecentLimit:  DefaultDiscoverGlobalRecentLimit,
		DiscoverProjectRecentLimit: DefaultDiscoverProjectRecentLimit,
		IndexSummaryMaxRunes:       DefaultIndexSummaryMaxRunes,
	}
}

// Validate verifies all configured bounds are within safe operational ranges.
func (s IndexSettings) Validate() error {
	if s.DiscoverPinnedLimit < MinDiscoverLimit || s.DiscoverPinnedLimit > MaxDiscoverPinnedLimit {
		return apperror.New(apperror.CodeInvalidArgument, fmt.Sprintf("discover_pinned_limit must be between %d and %d", MinDiscoverLimit, MaxDiscoverPinnedLimit))
	}
	if s.DiscoverGlobalRecentLimit < MinDiscoverLimit || s.DiscoverGlobalRecentLimit > MaxDiscoverGlobalLimit {
		return apperror.New(apperror.CodeInvalidArgument, fmt.Sprintf("discover_global_recent_limit must be between %d and %d", MinDiscoverLimit, MaxDiscoverGlobalLimit))
	}
	if s.DiscoverProjectRecentLimit < MinDiscoverLimit || s.DiscoverProjectRecentLimit > MaxDiscoverProjLimit {
		return apperror.New(apperror.CodeInvalidArgument, fmt.Sprintf("discover_project_recent_limit must be between %d and %d", MinDiscoverLimit, MaxDiscoverProjLimit))
	}
	if s.IndexSummaryMaxRunes < MinIndexSummaryRunes || s.IndexSummaryMaxRunes > MaxIndexSummaryRunes {
		return apperror.New(apperror.CodeInvalidArgument, fmt.Sprintf("index_summary_max_runes must be between %d and %d", MinIndexSummaryRunes, MaxIndexSummaryRunes))
	}
	return nil
}

// GetIndexSettings loads persisted discovery index settings or returns defaults when unconfigured.
func (s *Service) GetIndexSettings(ctx context.Context) (IndexSettings, error) {
	settings := DefaultIndexSettings()
	if s == nil || s.pool == nil {
		return settings, nil
	}

	var updatedAt pgtype.Timestamptz
	err := s.pool.QueryRow(ctx, `
		SELECT discover_pinned_limit, discover_global_recent_limit, discover_project_recent_limit, index_summary_max_runes, updated_at
		FROM memory_index_settings
		WHERE id = 'default'
	`).Scan(
		&settings.DiscoverPinnedLimit,
		&settings.DiscoverGlobalRecentLimit,
		&settings.DiscoverProjectRecentLimit,
		&settings.IndexSummaryMaxRunes,
		&updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultIndexSettings(), nil
	}
	if err != nil {
		return settings, apperror.Wrap(apperror.CodeInternalError, "query memory index settings", err)
	}
	if updatedAt.Valid {
		t := updatedAt.Time
		settings.UpdatedAt = &t
	}
	return settings, nil
}

// UpdateIndexSettings validates and persists discovery index settings across all server replicas.
func (s *Service) UpdateIndexSettings(ctx context.Context, req UpdateIndexSettingsRequest) (IndexSettings, error) {
	if req.DiscoverPinnedLimit == nil &&
		req.DiscoverGlobalRecentLimit == nil &&
		req.DiscoverProjectRecentLimit == nil &&
		req.IndexSummaryMaxRunes == nil {
		return IndexSettings{}, apperror.New(apperror.CodeInvalidArgument, "at least one setting field must be provided")
	}
	if s == nil || s.pool == nil {
		return IndexSettings{}, apperror.New(apperror.CodeInternalError, "memory service not initialized")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return IndexSettings{}, apperror.Wrap(apperror.CodeInternalError, "begin memory index settings tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO memory_index_settings (id) VALUES ('default') ON CONFLICT (id) DO NOTHING
	`); err != nil {
		return IndexSettings{}, apperror.Wrap(apperror.CodeInternalError, "ensure memory index settings row", err)
	}

	current := DefaultIndexSettings()
	if err := tx.QueryRow(ctx, `
		SELECT discover_pinned_limit, discover_global_recent_limit, discover_project_recent_limit, index_summary_max_runes
		FROM memory_index_settings
		WHERE id = 'default'
		FOR UPDATE
	`).Scan(
		&current.DiscoverPinnedLimit,
		&current.DiscoverGlobalRecentLimit,
		&current.DiscoverProjectRecentLimit,
		&current.IndexSummaryMaxRunes,
	); err != nil {
		return IndexSettings{}, apperror.Wrap(apperror.CodeInternalError, "lock memory index settings", err)
	}

	if req.DiscoverPinnedLimit != nil {
		current.DiscoverPinnedLimit = *req.DiscoverPinnedLimit
	}
	if req.DiscoverGlobalRecentLimit != nil {
		current.DiscoverGlobalRecentLimit = *req.DiscoverGlobalRecentLimit
	}
	if req.DiscoverProjectRecentLimit != nil {
		current.DiscoverProjectRecentLimit = *req.DiscoverProjectRecentLimit
	}
	if req.IndexSummaryMaxRunes != nil {
		current.IndexSummaryMaxRunes = *req.IndexSummaryMaxRunes
	}
	if err := current.Validate(); err != nil {
		return IndexSettings{}, err
	}

	var updatedAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE memory_index_settings
		SET discover_pinned_limit = $1,
		    discover_global_recent_limit = $2,
		    discover_project_recent_limit = $3,
		    index_summary_max_runes = $4,
		    updated_at = now()
		WHERE id = 'default'
		RETURNING updated_at
	`,
		current.DiscoverPinnedLimit,
		current.DiscoverGlobalRecentLimit,
		current.DiscoverProjectRecentLimit,
		current.IndexSummaryMaxRunes,
	).Scan(&updatedAt)
	if err != nil {
		return IndexSettings{}, apperror.Wrap(apperror.CodeInternalError, "save memory index settings", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IndexSettings{}, apperror.Wrap(apperror.CodeInternalError, "commit memory index settings", err)
	}
	current.UpdatedAt = &updatedAt
	return current, nil
}
