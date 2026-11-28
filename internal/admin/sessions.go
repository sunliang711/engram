package admin

import (
	"context"
	"strings"
	"time"

	"engram/internal/apperror"

	"github.com/jackc/pgx/v5"
)

// SessionMetadata 保存登录请求中的设备信息，不信任客户端提交的转发地址。
type SessionMetadata struct {
	ClientIP  string
	UserAgent string
}

type sessionView struct {
	ID         string    `json:"id"`
	ClientIP   string    `json:"client_ip"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

func (m *AuthManager) createSession(ctx context.Context, tx pgx.Tx, username string, version int64, metadata []SessionMetadata) (string, string, error) {
	token, err := m.signSession(username, version)
	if err != nil {
		return "", "", err
	}
	claims, err := m.verifySessionToken(token)
	if err != nil {
		return "", "", err
	}
	var device SessionMetadata
	if len(metadata) > 0 {
		device = metadata[0]
	}
	if len(device.UserAgent) > 1024 {
		device.UserAgent = strings.ToValidUTF8(device.UserAgent[:1024], "")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM admin_sessions WHERE username = $1 AND expires_at <= now()`, username); err != nil {
		return "", "", apperror.Wrap(apperror.CodeInternalError, "clean expired admin sessions", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO admin_sessions
		(id, username, password_version, client_ip, user_agent, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6, $7)`, claims.SessionID, username, version,
		device.ClientIP, device.UserAgent, time.Unix(claims.IssuedAt, 0).UTC(), time.Unix(claims.ExpiresAt, 0).UTC())
	if err != nil {
		return "", "", apperror.Wrap(apperror.CodeInternalError, "create admin session", err)
	}
	return token, claims.SessionID, nil
}

func (m *AuthManager) ListSessions(ctx context.Context, actor AdminState) ([]sessionView, error) {
	rows, err := m.pool.Query(ctx, `SELECT s.id, s.client_ip, s.user_agent, s.created_at, s.last_seen_at, s.expires_at
		FROM admin_sessions s JOIN admin_accounts a ON a.username = s.username
		WHERE s.username = $1 AND s.expires_at > now() AND s.password_version = a.password_version AND NOT a.disabled
		ORDER BY (s.id = $2) DESC, s.last_seen_at DESC, s.id`, actor.Username, actor.SessionID)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "list admin sessions", err)
	}
	defer rows.Close()
	items := make([]sessionView, 0)
	for rows.Next() {
		var item sessionView
		if err := rows.Scan(&item.ID, &item.ClientIP, &item.UserAgent, &item.CreatedAt, &item.LastSeenAt, &item.ExpiresAt); err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "scan admin session", err)
		}
		item.Current = item.ID == actor.SessionID
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "read admin sessions", err)
	}
	return items, nil
}

func (m *AuthManager) RevokeSession(ctx context.Context, actor AdminState, id string) error {
	// 会话管理仅限当前账号；重复撤销也成功，且不泄露其他账号的会话信息。
	if _, err := m.pool.Exec(ctx, `DELETE FROM admin_sessions WHERE id = $1 AND username = $2`, id, actor.Username); err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "revoke admin session", err)
	}
	return nil
}
