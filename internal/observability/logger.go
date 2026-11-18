package observability

import (
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

var credentialURLPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/@\s]+@)`)

// NewLogger creates an English structured zerolog.Logger with UTC timestamps.
func NewLogger(level string, out io.Writer) zerolog.Logger {
	if out == nil {
		out = os.Stdout
	}
	lvl, err := zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(level)))
	if err != nil {
		lvl = zerolog.InfoLevel
	}
	zerolog.TimeFieldFormat = time.RFC3339Nano
	zerolog.TimestampFunc = func() time.Time {
		return time.Now().UTC()
	}
	return zerolog.New(out).Level(lvl).With().Timestamp().Str("service", "ai-memory-server").Logger()
}

// SanitizeRemoteURL strips any embedded user:password or token from a Git remote URL before logging.
func SanitizeRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.User != nil {
			u.User = nil
			return u.String()
		}
	}
	return credentialURLPattern.ReplaceAllString(raw, "${1}")
}
