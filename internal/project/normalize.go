package project

import (
	"net/url"
	"regexp"
	"strings"

	"ai-memory/internal/apperror"
)

var (
	scpLikeRemoteRe = regexp.MustCompile(`^(?:([^@/\s]+)@)?([a-zA-Z0-9.-]+):(.+)$`)
	multiSlashRe    = regexp.MustCompile(`/+`)
)

// NormalizeRemote canonicalizes a Git remote URL according to Section 5.2:
// - Host converted to lowercase
// - Protocol, user info, port, and trailing .git stripped
// - Duplicate and trailing slashes cleaned
// - Repository path case preserved (unless host is known case-insensitive and configured, here preserved by default)
func NormalizeRemote(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", apperror.New(apperror.CodeInvalidArgument, "remote url cannot be empty")
	}

	var host, repoPath string

	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", apperror.New(apperror.CodeInvalidArgument, "invalid remote url")
		}
		host = strings.ToLower(u.Hostname())
		repoPath = u.Path
	} else if m := scpLikeRemoteRe.FindStringSubmatch(s); m != nil {
		host = strings.ToLower(m[2])
		repoPath = m[3]
	} else {
		// Already in canonical form like github.com/owner/repo
		parts := strings.SplitN(s, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", apperror.New(apperror.CodeInvalidArgument, "invalid remote url format")
		}
		host = strings.ToLower(strings.Split(parts[0], ":")[0])
		repoPath = parts[1]
	}

	if host == "" {
		return "", apperror.New(apperror.CodeInvalidArgument, "remote host cannot be empty")
	}

	repoPath = multiSlashRe.ReplaceAllString(repoPath, "/")
	repoPath = strings.TrimPrefix(repoPath, "/")
	repoPath = strings.TrimSuffix(repoPath, "/")
	repoPath = strings.TrimSuffix(repoPath, ".git")
	repoPath = strings.TrimSuffix(repoPath, "/")

	if repoPath == "" || strings.Contains(repoPath, "..") {
		return "", apperror.New(apperror.CodeInvalidArgument, "invalid repository path in remote url")
	}

	return host + "/" + repoPath, nil
}

// NormalizeRelPath validates and normalizes a workspace_path or marker_path relative to the Git root.
func NormalizeRelPath(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "." {
		return "", nil
	}
	if strings.HasPrefix(s, "/") || strings.Contains(s, "\\") {
		return "", apperror.New(apperror.CodeInvalidArgument, "relative path must not start with '/' or contain '\\'")
	}
	s = multiSlashRe.ReplaceAllString(s, "/")
	s = strings.TrimSuffix(s, "/")
	segments := strings.Split(s, "/")
	for _, seg := range segments {
		if seg == ".." || seg == "." || seg == "" {
			return "", apperror.New(apperror.CodeInvalidArgument, "relative path must not contain '.' or '..' segments")
		}
	}
	return s, nil
}

// PathMatchesPrefix returns true if targetPath is within or equal to registeredPrefix.
// An empty registeredPrefix matches the entire repository.
func PathMatchesPrefix(targetPath, registeredPrefix string) bool {
	if registeredPrefix == "" {
		return true
	}
	if targetPath == registeredPrefix {
		return true
	}
	return strings.HasPrefix(targetPath, registeredPrefix+"/")
}

// PathDepth returns the specificity score of a relative path (0 for root "", 1+ for nested paths).
func PathDepth(relPath string) int {
	if relPath == "" {
		return 0
	}
	return len(strings.Split(relPath, "/"))
}
