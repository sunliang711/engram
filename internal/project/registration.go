package project

import (
	"context"
	"errors"

	"engram/internal/apperror"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// LockRegistration serializes tenant identity writes across MCP and administrator entry points.
func LockRegistration(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('project-register:' || $1::text, 0))`, tenantID.String()); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return apperror.Wrap(apperror.CodeLockTimeout, "project registration is busy; retry with the same idempotency_key", err)
		}
		return apperror.Wrap(apperror.CodeInternalError, "lock project registration", err)
	}
	return nil
}
