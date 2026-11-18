package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"ai-memory/internal/apperror"

	"github.com/google/uuid"
)

const (
	ModeProject    = "project"
	ModeGlobalOnly = "global_only"
)

// ProjectContextClaims represents the signed payload inside an opaque project_context token.
type ProjectContextClaims struct {
	TenantID    uuid.UUID  `json:"t"`
	ProjectID   *uuid.UUID `json:"p,omitempty"`
	PrincipalID uuid.UUID  `json:"u"`
	Mode        string     `json:"m"`
	IssuedAt    int64      `json:"iat"`
	ExpiresAt   int64      `json:"exp"`
}

// CursorClaims represents the signed payload inside an opaque change cursor.
type CursorClaims struct {
	TenantID  uuid.UUID  `json:"t"`
	ProjectID *uuid.UUID `json:"p,omitempty"`
	Revision  int64      `json:"r"`
}

// TokenSigner signs and verifies stateless HMAC-SHA256 project_context and cursor tokens.
type TokenSigner struct {
	key []byte
	ttl time.Duration
}

func NewTokenSigner(secret string, ttl time.Duration) *TokenSigner {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &TokenSigner{
		key: []byte(secret),
		ttl: ttl,
	}
}

// SignProjectContext creates an opaque signed project_context token.
func (s *TokenSigner) SignProjectContext(tenantID uuid.UUID, projectID *uuid.UUID, principalID uuid.UUID, mode string) (string, error) {
	now := time.Now().UTC()
	claims := ProjectContextClaims{
		TenantID:    tenantID,
		ProjectID:   projectID,
		PrincipalID: principalID,
		Mode:        mode,
		IssuedAt:    now.Unix(),
		ExpiresAt:   now.Add(s.ttl).Unix(),
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", apperror.Wrap(apperror.CodeInternalError, "marshal project context", err)
	}
	return "ctx_" + s.signBytes(raw), nil
}

// VerifyProjectContext validates signature, expiration, and binding to the current authenticated principal.
func (s *TokenSigner) VerifyProjectContext(token string, expectedTenantID, expectedPrincipalID uuid.UUID) (*ProjectContextClaims, error) {
	if !strings.HasPrefix(token, "ctx_") {
		return nil, apperror.New(apperror.CodeProjectContextInvalid, "invalid project_context prefix")
	}
	raw, err := s.verifyBytes(strings.TrimPrefix(token, "ctx_"))
	if err != nil {
		return nil, apperror.New(apperror.CodeProjectContextInvalid, "invalid project_context signature")
	}
	var claims ProjectContextClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, apperror.New(apperror.CodeProjectContextInvalid, "malformed project_context payload")
	}
	if time.Now().UTC().Unix() > claims.ExpiresAt {
		return nil, apperror.New(apperror.CodeProjectContextInvalid, "project_context has expired; call memory_discover again")
	}
	if claims.TenantID != expectedTenantID || claims.PrincipalID != expectedPrincipalID {
		return nil, apperror.New(apperror.CodeProjectContextInvalid, "project_context does not match authenticated principal")
	}
	switch claims.Mode {
	case ModeProject:
		if claims.ProjectID == nil {
			return nil, apperror.New(apperror.CodeProjectContextInvalid, "project mode context missing project_id")
		}
	case ModeGlobalOnly:
		claims.ProjectID = nil
	default:
		return nil, apperror.New(apperror.CodeProjectContextInvalid, "invalid project_context mode")
	}
	return &claims, nil
}

// SignCursor creates an opaque signed cursor bound to tenantID and optional projectID.
func (s *TokenSigner) SignCursor(tenantID uuid.UUID, projectID *uuid.UUID, revision int64) (string, error) {
	claims := CursorClaims{
		TenantID:  tenantID,
		ProjectID: projectID,
		Revision:  revision,
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", apperror.Wrap(apperror.CodeInternalError, "marshal cursor", err)
	}
	return "cur_" + s.signBytes(raw), nil
}

// VerifyCursor validates an opaque cursor and ensures it belongs to the same tenant and project scope.
func (s *TokenSigner) VerifyCursor(cursor string, expectedTenantID uuid.UUID, expectedProjectID *uuid.UUID) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	if !strings.HasPrefix(cursor, "cur_") {
		return 0, apperror.New(apperror.CodeInvalidArgument, "invalid cursor format")
	}
	raw, err := s.verifyBytes(strings.TrimPrefix(cursor, "cur_"))
	if err != nil {
		return 0, apperror.New(apperror.CodeInvalidArgument, "invalid cursor signature")
	}
	var claims CursorClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return 0, apperror.New(apperror.CodeInvalidArgument, "malformed cursor payload")
	}
	if claims.TenantID != expectedTenantID {
		return 0, apperror.New(apperror.CodeInvalidArgument, "cursor tenant mismatch")
	}
	if (claims.ProjectID == nil) != (expectedProjectID == nil) {
		return 0, apperror.New(apperror.CodeInvalidArgument, "cursor project scope mismatch")
	}
	if claims.ProjectID != nil && *claims.ProjectID != *expectedProjectID {
		return 0, apperror.New(apperror.CodeInvalidArgument, "cursor project mismatch")
	}
	if claims.Revision < 0 {
		return 0, apperror.New(apperror.CodeInvalidArgument, "invalid cursor revision")
	}
	return claims.Revision, nil
}

func (s *TokenSigner) signBytes(payload []byte) string {
	b64Payload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(b64Payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return b64Payload + "." + sig
}

func (s *TokenSigner) verifyBytes(encoded string) ([]byte, error) {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "invalid token parts")
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(parts[0]))
	expectedSig := mac.Sum(nil)
	actualSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(actualSig, expectedSig) {
		return nil, apperror.New(apperror.CodeInvalidArgument, "signature mismatch")
	}
	return base64.RawURLEncoding.DecodeString(parts[0])
}
