package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"ai-memory/internal/apperror"
	"ai-memory/internal/config"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ctxKey struct{}

// Principal represents an authenticated caller and their verified tenant role.
type Principal struct {
	TenantID    uuid.UUID `json:"tenant_id"`
	PrincipalID uuid.UUID `json:"principal_id"`
	TenantRole  string    `json:"tenant_role"`
	DisplayName string    `json:"display_name"`
}

// WithPrincipal attaches Principal to ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext retrieves Principal from ctx.
func FromContext(ctx context.Context) (Principal, error) {
	v, ok := ctx.Value(ctxKey{}).(Principal)
	if !ok || v.TenantID == uuid.Nil || v.PrincipalID == uuid.Nil {
		return Principal{}, apperror.New(apperror.CodeUnauthenticated, "missing authenticated principal")
	}
	return v, nil
}

// HashToken returns the SHA-256 hex digest of a raw API token.
func HashToken(rawToken string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(rawToken)))
	return hex.EncodeToString(sum[:])
}

type staticTokenEntry struct {
	tenantID    uuid.UUID
	principalID uuid.UUID
}

// Authenticator authenticates callers and performs live RBAC checks against PostgreSQL.
type Authenticator struct {
	pool         *pgxpool.Pool
	cfg          config.AuthConfig
	staticTokens map[string]staticTokenEntry
}

func NewAuthenticator(pool *pgxpool.Pool, cfg config.AuthConfig) (*Authenticator, error) {
	st := make(map[string]staticTokenEntry, len(cfg.StaticTokens))
	for _, entry := range cfg.StaticTokens {
		if strings.TrimSpace(entry.Token) == "" {
			continue
		}
		tid, err := uuid.Parse(entry.TenantID)
		if err != nil {
			return nil, err
		}
		pid, err := uuid.Parse(entry.PrincipalID)
		if err != nil {
			return nil, err
		}
		st[HashToken(entry.Token)] = staticTokenEntry{
			tenantID:    tid,
			principalID: pid,
		}
	}
	return &Authenticator{
		pool:         pool,
		cfg:          cfg,
		staticTokens: st,
	}, nil
}

// AuthenticateRequest resolves the caller identity from Bearer token, OIDC headers, or mTLS headers,
// and verifies active status and tenant_members role in PostgreSQL.
func (a *Authenticator) AuthenticateRequest(ctx context.Context, r *http.Request) (Principal, error) {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		rawToken := strings.TrimSpace(authHeader[7:])
		if rawToken == "" {
			return Principal{}, apperror.New(apperror.CodeUnauthenticated, "empty bearer token")
		}
		return a.AuthenticateToken(ctx, rawToken)
	}

	if a.cfg.EnableOIDCHeaders {
		tidStr := strings.TrimSpace(r.Header.Get("X-OIDC-Tenant-ID"))
		sub := strings.TrimSpace(r.Header.Get("X-OIDC-Subject"))
		if tidStr != "" && sub != "" {
			return a.authenticateByProviderSubject(ctx, tidStr, "oidc", sub)
		}
	}

	if a.cfg.EnableMTLSHeaders {
		tidStr := strings.TrimSpace(r.Header.Get("X-MTLS-Tenant-ID"))
		sub := strings.TrimSpace(r.Header.Get("X-MTLS-Client-Subject"))
		if tidStr != "" && sub != "" {
			return a.authenticateByProviderSubject(ctx, tidStr, "mtls", sub)
		}
	}

	return Principal{}, apperror.New(apperror.CodeUnauthenticated, "authentication required")
}

// AuthenticateToken authenticates a raw Bearer API token against static config or api_tokens table.
func (a *Authenticator) AuthenticateToken(ctx context.Context, rawToken string) (Principal, error) {
	tokenHash := HashToken(rawToken)
	if st, ok := a.staticTokens[tokenHash]; ok {
		return a.VerifyPrincipalMembership(ctx, st.tenantID, st.principalID)
	}

	var tenantID, principalID uuid.UUID
	var expiresAt, revokedAt *time.Time
	err := a.pool.QueryRow(ctx, `
		SELECT tenant_id, principal_id, expires_at, revoked_at
		FROM api_tokens
		WHERE token_hash = $1
	`, tokenHash).Scan(&tenantID, &principalID, &expiresAt, &revokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Principal{}, apperror.New(apperror.CodeUnauthenticated, "invalid api token")
		}
		return Principal{}, apperror.Wrap(apperror.CodeInternalError, "query api token", err)
	}
	if revokedAt != nil {
		return Principal{}, apperror.New(apperror.CodeUnauthenticated, "api token has been revoked")
	}
	if expiresAt != nil && time.Now().UTC().After(*expiresAt) {
		return Principal{}, apperror.New(apperror.CodeUnauthenticated, "api token has expired")
	}
	return a.VerifyPrincipalMembership(ctx, tenantID, principalID)
}

func (a *Authenticator) authenticateByProviderSubject(ctx context.Context, tenantIDStr, provider, subject string) (Principal, error) {
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		return Principal{}, apperror.New(apperror.CodeUnauthenticated, "invalid tenant id")
	}
	var principalID uuid.UUID
	err = a.pool.QueryRow(ctx, `
		SELECT id FROM principals
		WHERE identity_provider = $1 AND subject = $2 AND status = 'active'
	`, provider, subject).Scan(&principalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Principal{}, apperror.New(apperror.CodeUnauthenticated, "unknown or disabled principal")
		}
		return Principal{}, apperror.Wrap(apperror.CodeInternalError, "query principal by subject", err)
	}
	return a.VerifyPrincipalMembership(ctx, tenantID, principalID)
}

// VerifyPrincipalMembership checks that the tenant and principal are active and returns the current tenant role.
func (a *Authenticator) VerifyPrincipalMembership(ctx context.Context, tenantID, principalID uuid.UUID) (Principal, error) {
	var p Principal
	err := a.pool.QueryRow(ctx, `
		SELECT t.id, pr.id, tm.role, pr.display_name
		FROM tenant_members tm
		JOIN tenants t ON t.id = tm.tenant_id
		JOIN principals pr ON pr.id = tm.principal_id
		WHERE tm.tenant_id = $1
		  AND tm.principal_id = $2
		  AND t.status = 'active'
		  AND pr.status = 'active'
	`, tenantID, principalID).Scan(&p.TenantID, &p.PrincipalID, &p.TenantRole, &p.DisplayName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Principal{}, apperror.New(apperror.CodeUnauthenticated, "principal is not an active member of tenant")
		}
		return Principal{}, apperror.Wrap(apperror.CodeInternalError, "verify tenant membership", err)
	}
	return p, nil
}

// GetProjectRole queries the caller's live project role in PostgreSQL.
// Tenant admins automatically receive 'maintainer' privileges on all active projects in their tenant.
func (a *Authenticator) GetProjectRole(ctx context.Context, tenantID, projectID, principalID uuid.UUID, tenantRole string) (string, error) {
	var projectActive bool
	err := a.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM projects
			WHERE id = $1 AND tenant_id = $2 AND status = 'active'
		)
	`, projectID, tenantID).Scan(&projectActive)
	if err != nil {
		return "", apperror.Wrap(apperror.CodeInternalError, "check project active status", err)
	}
	if !projectActive {
		return "", nil
	}

	if tenantRole == "admin" {
		return "maintainer", nil
	}

	var role string
	err = a.pool.QueryRow(ctx, `
		SELECT role
		FROM project_members
		WHERE project_id = $1 AND principal_id = $2
	`, projectID, principalID).Scan(&role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", apperror.Wrap(apperror.CodeInternalError, "query project membership", err)
	}
	return role, nil
}

func CanReadGlobal(tenantRole string) bool {
	switch tenantRole {
	case "member", "global_writer", "admin":
		return true
	default:
		return false
	}
}

func CanWriteGlobal(tenantRole string) bool {
	switch tenantRole {
	case "global_writer", "admin":
		return true
	default:
		return false
	}
}

func CanReadProject(projectRole string) bool {
	switch projectRole {
	case "reader", "writer", "maintainer":
		return true
	default:
		return false
	}
}

func CanWriteProject(projectRole string) bool {
	switch projectRole {
	case "writer", "maintainer":
		return true
	default:
		return false
	}
}

func CanMaintainProject(projectRole string) bool {
	return projectRole == "maintainer"
}
