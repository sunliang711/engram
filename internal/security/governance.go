package security

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"

	"engram/internal/apperror"
	"engram/internal/config"
)

// SourceRef is a controlled source reference entry stored in memories.source_refs.
type SourceRef struct {
	Type  string `json:"type"`
	Ref   string `json:"ref"`
	Title string `json:"title,omitempty"`
}

var (
	categoryRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	tagRe      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,63}$`)

	dangerousProtocolRe = regexp.MustCompile(`(?i)\]\(\s*(javascript:|vbscript:|data:text/html|file:)`)

	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}\b`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}\b`),
		regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{24,}\b`),
		regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@[^\s/]+`),
	}
)

// ContentHash computes a deterministic SHA-256 hex digest of normalized Markdown content.
func ContentHash(content string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(content), "\r\n", "\n")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// ValidateCategory checks that category matches ^[a-z0-9][a-z0-9_-]{0,31}$.
func ValidateCategory(category string) error {
	if !categoryRe.MatchString(category) {
		return apperror.New(apperror.CodeInvalidArgument, "category must match ^[a-z0-9][a-z0-9_-]{0,31}$")
	}
	return nil
}

// ValidateStatus checks that status is one of active, deprecated, archived.
func ValidateStatus(status string) error {
	switch status {
	case "active", "deprecated", "archived":
		return nil
	default:
		return apperror.New(apperror.CodeInvalidArgument, "status must be one of: active, deprecated, archived")
	}
}

// ValidateScope checks that scope is one of global or project.
func ValidateScope(scope string) error {
	switch scope {
	case "global", "project":
		return nil
	default:
		return apperror.New(apperror.CodeInvalidArgument, "scope must be 'global' or 'project'")
	}
}

// ValidateMemoryContent checks title, content, tags, and source_refs against size, protocol, and secret rules.
func ValidateMemoryContent(limits config.LimitsConfig, title, content string, tags []string, refs []SourceRef) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return apperror.New(apperror.CodeInvalidArgument, "title cannot be empty")
	}
	if len(title) > limits.MaxTitleBytes {
		return apperror.New(apperror.CodeInvalidArgument, "title exceeds maximum allowed length")
	}
	if strings.TrimSpace(content) == "" {
		return apperror.New(apperror.CodeInvalidArgument, "content cannot be empty")
	}
	if len(content) > limits.MaxContentBytes {
		return apperror.New(apperror.CodeInvalidArgument, "content exceeds maximum allowed size")
	}
	if err := CheckTextSafety(title); err != nil {
		return err
	}
	if err := CheckTextSafety(content); err != nil {
		return err
	}
	if err := ValidateTags(limits, tags); err != nil {
		return err
	}
	if err := ValidateSourceRefs(limits, refs); err != nil {
		return err
	}
	return nil
}

// ValidateTags validates a slice of tags.
func ValidateTags(limits config.LimitsConfig, tags []string) error {
	if len(tags) > limits.MaxTags {
		return apperror.New(apperror.CodeInvalidArgument, "too many tags")
	}
	for _, t := range tags {
		if len(t) == 0 || len(t) > limits.MaxTagBytes || !tagRe.MatchString(t) {
			return apperror.New(apperror.CodeInvalidArgument, "invalid tag format")
		}
	}
	return nil
}

// ValidateSourceRefs validates controlled source references and ensures no credentials or dangerous URLs are present.
func ValidateSourceRefs(limits config.LimitsConfig, refs []SourceRef) error {
	if len(refs) > limits.MaxSourceRefs {
		return apperror.New(apperror.CodeInvalidArgument, "too many source_refs")
	}
	for _, r := range refs {
		switch r.Type {
		case "task", "file", "url", "commit", "doc":
		default:
			return apperror.New(apperror.CodeInvalidArgument, "source_ref.type must be one of: task, file, url, commit, doc")
		}
		refVal := strings.TrimSpace(r.Ref)
		if refVal == "" || len(refVal) > 512 {
			return apperror.New(apperror.CodeInvalidArgument, "source_ref.ref must be non-empty and <= 512 bytes")
		}
		if len(r.Title) > 256 {
			return apperror.New(apperror.CodeInvalidArgument, "source_ref.title exceeds 256 bytes")
		}
		if err := CheckTextSafety(refVal); err != nil {
			return err
		}
		if err := CheckTextSafety(r.Title); err != nil {
			return err
		}
		if r.Type == "url" || strings.Contains(refVal, "://") {
			u, err := url.Parse(refVal)
			if err != nil {
				return apperror.New(apperror.CodeInvalidArgument, "invalid url in source_ref")
			}
			scheme := strings.ToLower(u.Scheme)
			if scheme != "http" && scheme != "https" {
				return apperror.New(apperror.CodeInvalidArgument, "source_ref url must use http or https scheme")
			}
			if u.User != nil {
				return apperror.New(apperror.CodeInvalidArgument, "source_ref url must not contain embedded credentials")
			}
		}
	}
	return nil
}

// CheckTextSafety scans text for dangerous Markdown link protocols and credential/key patterns.
func CheckTextSafety(text string) error {
	if dangerousProtocolRe.MatchString(text) {
		return apperror.New(apperror.CodeInvalidArgument, "markdown contains forbidden link protocol")
	}
	for _, pat := range secretPatterns {
		if pat.MatchString(text) {
			return apperror.New(apperror.CodeInvalidArgument, "content contains prohibited credential, private key, or secret token")
		}
	}
	return nil
}
