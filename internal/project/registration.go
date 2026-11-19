package project

import (
	"context"

	"engram/internal/apperror"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LockRegistration serializes tenant identity writes across MCP and administrator entry points.
func LockRegistration(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('project-register:' || $1::text, 0))`, tenantID.String()); err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "lock project registration", err)
	}
	return nil
}
