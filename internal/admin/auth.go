package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"ai-memory/internal/apperror"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	AdminUsername     = "admin"
	SessionCookieName = "ai_memory_admin_session"
	pbkdf2Iterations  = 60000
)

// AdminSessionClaims is the payload signed inside the admin session cookie/token.
type AdminSessionClaims struct {
	Username        string `json:"u"`
	PasswordVersion int64  `json:"pv"`
	IssuedAt        int64  `json:"iat"`
	ExpiresAt       int64  `json:"exp"`
}

// AdminState represents the live verified state of the admin session.
type AdminState struct {
	Authenticated       bool   `json:"authenticated"`
	Username            string `json:"username,omitempty"`
	MustChangePassword  bool   `json:"must_change_password"`
	ResetAdminEnvActive bool   `json:"reset_admin_env_active"`
}

// AuthManager manages the Web UI admin account, bootstrap password, and session tokens.
type AuthManager struct {
	pool       *pgxpool.Pool
	signKey    []byte
	sessionTTL time.Duration
	resetAdmin bool
}

func NewAuthManager(pool *pgxpool.Pool, signKey string, sessionTTL time.Duration, resetAdmin bool) *AuthManager {
	if sessionTTL <= 0 {
		sessionTTL = 24 * time.Hour
	}
	return &AuthManager{
		pool:       pool,
		signKey:    []byte("admin-session:" + signKey),
		sessionTTL: sessionTTL,
		resetAdmin: resetAdmin,
	}
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
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ai-memory-admin-bootstrap', 0))`); err != nil {
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
			    password_version = password_version + 1,
			    updated_at = $2
			WHERE username = $3
		`, passHash, now, AdminUsername)
		if err != nil {
			return "", fmt.Errorf("reset admin account password: %w", err)
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
	fmt.Fprintf(out, "  AI Shared Memory Server - Web UI Admin Credentials (%s)\n", reason)
	fmt.Fprintf(out, "  Username           : %s\n", AdminUsername)
	fmt.Fprintf(out, "  Temporary Password : %s\n", tempPassword)
	fmt.Fprintf(out, "  Action Required    : Sign in to the Web UI and set a new password.\n")
	fmt.Fprintf(out, "========================================================================\n\n")

	return tempPassword, nil
}

// Login verifies admin credentials and returns a signed session token and live AdminState.
func (m *AuthManager) Login(ctx context.Context, username, password string) (string, AdminState, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		username = AdminUsername
	}
	var (
		storedHash string
		mustChange bool
		pwdVersion int64
	)
	err := m.pool.QueryRow(ctx, `
		SELECT password_hash, must_change_password, password_version
		FROM admin_accounts
		WHERE username = $1
	`, username).Scan(&storedHash, &mustChange, &pwdVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", AdminState{}, apperror.New(apperror.CodeUnauthenticated, "invalid username or password")
		}
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "query admin account", err)
	}

	if !verifyPassword(password, storedHash) {
		return "", AdminState{}, apperror.New(apperror.CodeUnauthenticated, "invalid username or password")
	}

	token, err := m.signSession(username, pwdVersion)
	if err != nil {
		return "", AdminState{}, err
	}

	return token, AdminState{
		Authenticated:       true,
		Username:            username,
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
		pwdVersion int64
	)
	err = m.pool.QueryRow(ctx, `
		SELECT must_change_password, password_version
		FROM admin_accounts
		WHERE username = $1
	`, claims.Username).Scan(&mustChange, &pwdVersion)
	if err != nil {
		return AdminState{ResetAdminEnvActive: m.resetAdmin}, apperror.New(apperror.CodeUnauthenticated, "admin account not found")
	}
	if pwdVersion != claims.PasswordVersion {
		return AdminState{ResetAdminEnvActive: m.resetAdmin}, apperror.New(apperror.CodeUnauthenticated, "admin session expired due to password change")
	}

	return AdminState{
		Authenticated:       true,
		Username:            claims.Username,
		MustChangePassword:  mustChange,
		ResetAdminEnvActive: m.resetAdmin,
	}, nil
}

// ChangePassword updates the admin password, clears must_change_password, increments password_version,
// and returns a fresh session token and updated AdminState.
func (m *AuthManager) ChangePassword(ctx context.Context, username, newPassword string) (string, AdminState, error) {
	newPassword = strings.TrimSpace(newPassword)
	if len(newPassword) < 8 {
		return "", AdminState{}, apperror.New(apperror.CodeInvalidArgument, "new password must be at least 8 characters")
	}
	if len(newPassword) > 128 {
		return "", AdminState{}, apperror.New(apperror.CodeInvalidArgument, "new password must not exceed 128 characters")
	}

	passHash, err := hashPassword(newPassword)
	if err != nil {
		return "", AdminState{}, apperror.Wrap(apperror.CodeInternalError, "hash new password", err)
	}

	var newVersion int64
	err = m.pool.QueryRow(ctx, `
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

	token, err := m.signSession(username, newVersion)
	if err != nil {
		return "", AdminState{}, err
	}

	return token, AdminState{
		Authenticated:       true,
		Username:            username,
		MustChangePassword:  false,
		ResetAdminEnvActive: m.resetAdmin,
	}, nil
}

func (m *AuthManager) signSession(username string, pwdVersion int64) (string, error) {
	now := time.Now().UTC()
	claims := AdminSessionClaims{
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
	if time.Now().UTC().Unix() > claims.ExpiresAt {
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
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk := pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, 32)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", pbkdf2Iterations, hex.EncodeToString(salt), hex.EncodeToString(dk)), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expectedDK, err := hex.DecodeString(parts[3])
	if err != nil {
		return false
	}
	actualDK := pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, len(expectedDK))
	return subtle.ConstantTimeCompare(actualDK, expectedDK) == 1
}

// pbkdf2SHA256 implements RFC 2898 PBKDF2 with HMAC-SHA256 using only the Go standard library.
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen

	var buf [4]byte
	dk := make([]byte, 0, numBlocks*hashLen)
	u := make([]byte, hashLen)

	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		prf.Write(buf[:])
		dk = prf.Sum(dk)
		t := dk[len(dk)-hashLen:]
		copy(u, t)

		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = u[:0]
			u = prf.Sum(u)
			for x := 0; x < len(u); x++ {
				t[x] ^= u[x]
			}
		}
	}
	return dk[:keyLen]
}
