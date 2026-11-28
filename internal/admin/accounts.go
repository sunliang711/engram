package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	"engram/internal/apperror"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type accountView struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Role        string     `json:"role"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

type accountChange struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	NewPassword     string `json:"new_password"`
	Role            string `json:"role"`
	CurrentPassword string `json:"current_password"`
}

func (m *AuthManager) ListAccounts(ctx context.Context) ([]accountView, error) {
	rows, err := m.pool.Query(ctx, `SELECT id, username, role, disabled, created_at, last_login_at
		FROM admin_accounts ORDER BY created_at, username`)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "list console accounts", err)
	}
	defer rows.Close()
	items := make([]accountView, 0)
	for rows.Next() {
		var item accountView
		if err := rows.Scan(&item.ID, &item.Username, &item.Role, &item.Disabled, &item.CreatedAt, &item.LastLoginAt); err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "scan console account", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "read console accounts", err)
	}
	return items, nil
}

func validateAccountChange(action string, input accountChange) error {
	if action == "create" {
		if len(input.Username) < 3 || len(input.Username) > 64 {
			return apperror.New(apperror.CodeInvalidArgument, "username must be 3-64 ASCII letters, digits, dots, underscores or hyphens")
		}
		for _, c := range input.Username {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
				return apperror.New(apperror.CodeInvalidArgument, "username must be 3-64 ASCII letters, digits, dots, underscores or hyphens")
			}
		}
	}
	if (action == "create" || action == "role") && input.Role != "admin" && input.Role != "viewer" {
		return apperror.New(apperror.CodeInvalidArgument, "invalid console account role")
	}
	password := input.NewPassword
	if action == "create" {
		password = input.Password
	}
	if (action == "create" || action == "reset-password") && (len(strings.TrimSpace(password)) < 8 || len(password) > 72) {
		return apperror.New(apperror.CodeInvalidArgument, "password must be 8-72 bytes")
	}
	return nil
}

func (m *AuthManager) ChangeAccount(ctx context.Context, actor AdminState, id, action string, input accountChange) (string, error) {
	if err := validateAccountChange(action, input); err != nil {
		return "", err
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return "", apperror.Wrap(apperror.CodeInternalError, "begin console account change", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// 统一串行化角色与启停变更，防止并发操作移除最后一个可用管理员。
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('engram-admin-accounts', 0))`); err != nil {
		return "", apperror.Wrap(apperror.CodeInternalError, "lock console accounts", err)
	}
	var actorHash string
	err = tx.QueryRow(ctx, `SELECT a.password_hash FROM admin_accounts a JOIN admin_sessions s ON s.username = a.username
		WHERE a.username = $1 AND a.role = 'admin' AND NOT a.disabled AND NOT a.must_change_password
		  AND s.id = $2 AND s.password_version = a.password_version AND s.expires_at > now()
		FOR UPDATE OF a, s`, actor.Username, actor.SessionID).Scan(&actorHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", apperror.New(apperror.CodeUnauthorized, "administrator access required")
		}
		return "", apperror.Wrap(apperror.CodeInternalError, "verify acting administrator", err)
	}
	if !verifyPassword(input.CurrentPassword, actorHash) {
		return "", apperror.New(apperror.CodeInvalidArgument, "current password verification failed")
	}
	if action == "create" {
		hash, err := hashPassword(input.Password)
		if err != nil {
			return "", apperror.Wrap(apperror.CodeInternalError, "hash console password", err)
		}
		id = uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO admin_accounts(id, username, password_hash, must_change_password, role)
			VALUES ($1, $2, $3, false, $4)`, id, input.Username, hash, input.Role)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return "", apperror.New(apperror.CodeVersionConflict, "console username already exists")
			}
			return "", apperror.Wrap(apperror.CodeInternalError, "create console account", err)
		}
	} else {
		var username, role string
		var disabled bool
		err := tx.QueryRow(ctx, `SELECT username, role, disabled FROM admin_accounts WHERE id = $1 FOR UPDATE`, id).Scan(&username, &role, &disabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", apperror.New(apperror.CodeNotFound, "console account not found")
		}
		if err != nil {
			return "", apperror.Wrap(apperror.CodeInternalError, "query console account", err)
		}
		if action == "disable" && username == actor.Username {
			return "", apperror.New(apperror.CodeInvalidArgument, "cannot disable your current account")
		}
		if role == "admin" && !disabled && (action == "disable" || action == "role" && input.Role == "viewer") {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM admin_accounts WHERE role = 'admin' AND NOT disabled`).Scan(&count); err != nil {
				return "", apperror.Wrap(apperror.CodeInternalError, "count available administrators", err)
			}
			if count <= 1 {
				return "", apperror.New(apperror.CodeInvalidArgument, "cannot remove the last active administrator")
			}
		}
		keepSession := ""
		switch action {
		case "role":
			_, err = tx.Exec(ctx, `UPDATE admin_accounts SET role = $1, updated_at = now() WHERE id = $2`, input.Role, id)
		case "enable", "disable":
			_, err = tx.Exec(ctx, `UPDATE admin_accounts SET disabled = $1, updated_at = now() WHERE id = $2`, action == "disable", id)
		case "reset-password":
			var hash string
			hash, err = hashPassword(input.NewPassword)
			if err == nil {
				_, err = tx.Exec(ctx, `UPDATE admin_accounts SET password_hash = $1, must_change_password = false, updated_at = now() WHERE id = $2`, hash, id)
			}
			if username == actor.Username {
				keepSession = actor.SessionID
			}
		default:
			return "", apperror.New(apperror.CodeInvalidArgument, "invalid console account action")
		}
		if err != nil {
			return "", apperror.Wrap(apperror.CodeInternalError, "update console account", err)
		}
		if action != "enable" {
			// 自助重置密码保留当前会话，角色变更与停用撤销该账号全部会话。
			if _, err := tx.Exec(ctx, `DELETE FROM admin_sessions WHERE username = $1 AND id::text <> $2`, username, keepSession); err != nil {
				return "", apperror.Wrap(apperror.CodeInternalError, "revoke changed account sessions", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", apperror.Wrap(apperror.CodeInternalError, "commit console account change", err)
	}
	return id, nil
}
