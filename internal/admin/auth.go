package admin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"engram/internal/apperror"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	AdminUsername     = "admin"
	SessionCookieName = "engram_admin_session"
)

// AdminSessionClaims is the payload signed inside the admin session cookie/token.
type AdminSessionClaims struct {
	SessionID       string `json:"sid,omitempty"`
	Username        string `json:"u"`
	PasswordVersion int64  `json:"pv"`
	IssuedAt        int64  `json:"iat"`
	ExpiresAt       int64  `json:"exp"`
}

// AdminState represents the live verified state of the admin session.
type AdminState struct {
	Authenticated       bool   `json:"authenticated"`
	Username            string `json:"username,omitempty"`
	Role                string `json:"role,omitempty"`
	SessionID           string `json:"session_id,omitempty"`
	MustChangePassword  bool   `json:"must_change_password"`
	ResetAdminEnvActive bool   `json:"reset_admin_env_active"`
	Version             string `json:"version,omitempty"`
}

// AuthManager manages the Web UI admin account, bootstrap password, and session tokens.
type AuthManager struct {
	pool         *pgxpool.Pool
	signKey      []byte
	tokenEncKey  [32]byte
	sessionTTL   time.Duration
	resetAdmin   bool
	cookieSecure bool
}

func NewAuthManager(pool *pgxpool.Pool, signKey string, sessionTTL time.Duration, resetAdmin bool, cookieSecure ...bool) *AuthManager {
	if sessionTTL <= 0 {
		sessionTTL = 24 * time.Hour
	}
	secure := false
	if len(cookieSecure) > 0 {
		secure = cookieSecure[0]
	}
	return &AuthManager{
		pool:         pool,
		signKey:      []byte("admin-session:" + signKey),
		tokenEncKey:  sha256.Sum256([]byte("api-token-seal:" + signKey)),
		sessionTTL:   sessionTTL,
		resetAdmin:   resetAdmin,
		cookieSecure: secure,
	}
}

// SealToken encrypts a raw API token with AES-256-GCM so admins can view it again in the Web UI.
func (m *AuthManager) SealToken(raw string) (string, error) {
	block, err := aes.NewCipher(m.tokenEncKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(raw), nil)
	return "v1:" + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// UnsealToken decrypts a sealed API token produced by SealToken.
func (m *AuthManager) UnsealToken(sealed string) (string, error) {
	if !strings.HasPrefix(sealed, "v1:") {
		return "", errors.New("unsupported sealed token format")
	}
	rawBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, "v1:"))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(m.tokenEncKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(rawBytes) < nonceSize {
		return "", errors.New("invalid sealed token payload")
	}
	plaintext, err := gcm.Open(nil, rawBytes[:nonceSize], rawBytes[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (m *AuthManager) CookieSecure() bool {
	return m.cookieSecure
}

// Bootstrap initializes the admin account on first startup or resets it when RESET_ADMIN is active.
// If a temporary password is generated, it is printed to out and returned.
func (m *AuthManager) Bootstrap(ctx context.Context, out io.Writer) (string, error) {
	if out == nil {
		out = os.Stdout
	}

	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", fmt.Errorf("begin admin bootstrap tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize bootstrap across replicas
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('engram-admin-bootstrap', 0))`); err != nil {
		return "", fmt.Errorf("acquire admin bootstrap lock: %w", err)
	}

	var existingVersion int64
	err = tx.QueryRow(ctx, `
		SELECT password_version FROM admin_accounts WHERE username = $1
	`, AdminUsername).Scan(&existingVersion)

	exists := true
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			exists = false
		} else {
			return "", fmt.Errorf("query admin_accounts: %w", err)
		}
	}

	if exists && !m.resetAdmin {
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return "", nil
	}

	tempPassword, err := generateRandomPassword(20)
	if err != nil {
		return "", fmt.Errorf("generate temporary admin password: %w", err)
	}
	passHash, err := hashPassword(tempPassword)
	if err != nil {
		return "", fmt.Errorf("hash temporary admin password: %w", err)
	}

	now := time.Now().UTC()
	if !exists {
		_, err = tx.Exec(ctx, `
			INSERT INTO admin_accounts (username, password_hash, must_change_password, password_version, updated_at)
			VALUES ($1, $2, true, 1, $3)
		`, AdminUsername, passHash, now)
		if err != nil {
			return "", fmt.Errorf("insert initial admin account: %w", err)
		}
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE admin_accounts
			SET password_hash = $1,
			    must_change_password = true,
			    role = 'admin',
			    disabled = false,
			    password_version = password_version + 1,
			    updated_at = $2
			WHERE username = $3
		`, passHash, now, AdminUsername)
		if err != nil {
			return "", fmt.Errorf("reset admin account password: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM admin_sessions WHERE username = $1`, AdminUsername); err != nil {
			return "", fmt.Errorf("revoke reset admin sessions: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit admin bootstrap tx: %w", err)
	}

	reason := "Initial First-Boot Setup"
	if exists && m.resetAdmin {
		reason = "Admin Password Reset (RESET_ADMIN env var set)"
	}

	fmt.Fprintf(out, "\n========================================================================\n")
	fmt.Fprintf(out, "  Engram - Web UI Admin Credentials (%s)\n", reason)
	fmt.Fprintf(out, "  Username           : %s\n", AdminUsername)
	fmt.Fprintf(out, "  Temporary Password : %s\n", tempPassword)
	fmt.Fprintf(out, "  Action Required    : Sign in to the Web UI and set a new password.\n")
	fmt.Fprintf(out, "========================================================================\n\n")

	return tempPassword, nil
}

// Login verifies admin credentials and returns a signed session token and live AdminState.
func (m *AuthManager) Login(ctx context.Context, username, password string, metadata ...SessionMetadata) (string, AdminState, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		username = AdminUsername
	}
	var (
		storedHash string
		mustChange bool
		pwdVersion int64
		role       string
		disabled   bool
	)
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "begin admin login", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, `
		SELECT password_hash, must_change_password, password_version, role, disabled
		FROM admin_accounts
		WHERE username = $1
		FOR UPDATE
	`, username).Scan(&storedHash, &mustChange, &pwdVersion, &role, &disabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", AdminState{}, apperror.New(apperror.CodeUnauthenticated, "invalid username or password")
		}
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "query admin account", err)
	}

	if !verifyPassword(password, storedHash) || disabled {
		return "", AdminState{}, apperror.New(apperror.CodeUnauthenticated, "invalid username or password")
	}

	token, sessionID, err := m.createSession(ctx, tx, username, pwdVersion, metadata)
	if err != nil {
		return "", AdminState{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE admin_accounts SET last_login_at = now() WHERE username = $1`, username); err != nil {
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "update admin login time", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "commit admin login", err)
	}

	return token, AdminState{
		Authenticated:       true,
		Username:            username,
		Role:                role,
		SessionID:           sessionID,
		MustChangePassword:  mustChange,
		ResetAdminEnvActive: m.resetAdmin,
	}, nil
}

// VerifySession validates a session token against the current database password_version.
func (m *AuthManager) VerifySession(ctx context.Context, token string) (AdminState, error) {
	claims, err := m.verifySessionToken(token)
	if err != nil {
		return AdminState{ResetAdminEnvActive: m.resetAdmin}, err
	}

	var (
		mustChange bool
		role       string
	)
	err = m.pool.QueryRow(ctx, `
		SELECT a.must_change_password, a.role
		FROM admin_accounts a JOIN admin_sessions s ON s.username = a.username
		WHERE s.id = $1 AND a.username = $2 AND a.password_version = $3
		  AND s.password_version = a.password_version AND NOT a.disabled AND s.expires_at > now()
	`, claims.SessionID, claims.Username, claims.PasswordVersion).Scan(&mustChange, &role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminState{ResetAdminEnvActive: m.resetAdmin}, apperror.New(apperror.CodeUnauthenticated, "admin session expired or revoked")
		}
		return AdminState{}, apperror.Wrap(apperror.CodeInternalError, "verify admin session", err)
	}
	// 最近活动最多每分钟写入一次，撤销后只更新已有记录，禁止恢复会话。
	if _, err := m.pool.Exec(ctx, `UPDATE admin_sessions SET last_seen_at = now()
		WHERE id = $1 AND last_seen_at < now() - interval '1 minute'`, claims.SessionID); err != nil {
		return AdminState{}, apperror.Wrap(apperror.CodeInternalError, "touch admin session", err)
	}

	return AdminState{
		Authenticated:       true,
		Username:            claims.Username,
		Role:                role,
		SessionID:           claims.SessionID,
		MustChangePassword:  mustChange,
		ResetAdminEnvActive: m.resetAdmin,
	}, nil
}

// ChangePassword updates the admin password, clears must_change_password, increments password_version,
// and returns a fresh session token and updated AdminState.
// When must_change_password is false (voluntary change), currentPassword must be verified.
func (m *AuthManager) ChangePassword(ctx context.Context, username, currentPassword, newPassword string, metadata ...SessionMetadata) (string, AdminState, error) {
	newPassword = strings.TrimSpace(newPassword)
	if len(newPassword) < 8 {
		return "", AdminState{}, apperror.New(apperror.CodeInvalidArgument, "new password must be at least 8 characters")
	}
	if len(newPassword) > 72 {
		return "", AdminState{}, apperror.New(apperror.CodeInvalidArgument, "new password must not exceed 72 characters")
	}

	var (
		storedHash string
		mustChange bool
		role       string
	)
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "begin admin password change", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, `
		SELECT password_hash, must_change_password, role
		FROM admin_accounts
		WHERE username = $1 AND NOT disabled
		FOR UPDATE
	`, username).Scan(&storedHash, &mustChange, &role)
	if err != nil {
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "query admin account", err)
	}

	if !mustChange || currentPassword != "" {
		if strings.TrimSpace(currentPassword) == "" || !verifyPassword(currentPassword, storedHash) {
			return "", AdminState{}, apperror.New(apperror.CodeUnauthenticated, "current password is incorrect")
		}
	}

	passHash, err := hashPassword(newPassword)
	if err != nil {
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "hash new password", err)
	}

	var newVersion int64
	err = tx.QueryRow(ctx, `
		UPDATE admin_accounts
		SET password_hash = $1,
		    must_change_password = false,
		    password_version = password_version + 1,
		    updated_at = now()
		WHERE username = $2
		RETURNING password_version
	`, passHash, username).Scan(&newVersion)
	if err != nil {
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "update admin password", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM admin_sessions WHERE username = $1`, username); err != nil {
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "revoke admin sessions", err)
	}
	token, sessionID, err := m.createSession(ctx, tx, username, newVersion, metadata)
	if err != nil {
		return "", AdminState{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "commit admin password change", err)
	}

	return token, AdminState{
		Authenticated:       true,
		Username:            username,
		Role:                role,
		SessionID:           sessionID,
		MustChangePassword:  false,
		ResetAdminEnvActive: m.resetAdmin,
	}, nil
}

func (m *AuthManager) signSession(username string, pwdVersion int64) (string, error) {
	now := time.Now().UTC()
	claims := AdminSessionClaims{
		SessionID:       uuid.NewString(),
		Username:        username,
		PasswordVersion: pwdVersion,
		IssuedAt:        now.Unix(),
		ExpiresAt:       now.Add(m.sessionTTL).Unix(),
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", apperror.Wrap(apperror.CodeInternalError, "marshal admin session", err)
	}
	b64 := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, m.signKey)
	mac.Write([]byte(b64))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return "adm_" + b64 + "." + sig, nil
}

func (m *AuthManager) verifySessionToken(token string) (*AdminSessionClaims, error) {
	if !strings.HasPrefix(token, "adm_") {
		return nil, apperror.New(apperror.CodeUnauthenticated, "invalid admin session token")
	}
	parts := strings.Split(strings.TrimPrefix(token, "adm_"), ".")
	if len(parts) != 2 {
		return nil, apperror.New(apperror.CodeUnauthenticated, "malformed admin session token")
	}
	mac := hmac.New(sha256.New, m.signKey)
	mac.Write([]byte(parts[0]))
	expectedSig := mac.Sum(nil)
	actualSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(actualSig, expectedSig) {
		return nil, apperror.New(apperror.CodeUnauthenticated, "invalid admin session signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, apperror.New(apperror.CodeUnauthenticated, "invalid admin session encoding")
	}
	var claims AdminSessionClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, apperror.New(apperror.CodeUnauthenticated, "invalid admin session claims")
	}
	// 无独立编号的旧凭证必须重新登录，避免被撤销后再次登记。
	if _, err := uuid.Parse(claims.SessionID); err != nil || claims.Username == "" || claims.PasswordVersion < 1 {
		return nil, apperror.New(apperror.CodeUnauthenticated, "invalid admin session claims")
	}
	if time.Now().UTC().Unix() >= claims.ExpiresAt {
		return nil, apperror.New(apperror.CodeUnauthenticated, "admin session expired")
	}
	return &claims, nil
}

func generateRandomPassword(length int) (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, length)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "adm_" + string(out), nil
}

func hashPassword(password string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

func verifyPassword(password, encoded string) bool {
	return bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password)) == nil
}
