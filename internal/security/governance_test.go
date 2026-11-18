package security

import (
	"testing"

	"ai-memory/internal/config"
)

func TestValidateMemoryContent(t *testing.T) {
	limits := config.Default().Limits

	err := ValidateMemoryContent(limits, "PostgreSQL Transaction Rules", "Always acquire tenant advisory lock before event insert.", []string{"postgresql", "concurrency"}, []SourceRef{
		{Type: "file", Ref: "DESIGN.md", Title: "Design Document"},
		{Type: "url", Ref: "https://www.postgresql.org/docs/current/explicit-locking.html"},
	})
	if err != nil {
		t.Fatalf("expected valid content, got error: %v", err)
	}

	// Secret detection
	err = ValidateMemoryContent(limits, "Leak", "Here is token ghp_1234567890abcdefghijklmnopqrstuvwxyzABCD", nil, nil)
	if err == nil {
		t.Fatalf("expected secret detection error for GitHub token")
	}

	// Private key detection
	err = ValidateMemoryContent(limits, "Key", "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA", nil, nil)
	if err == nil {
		t.Fatalf("expected secret detection error for private key")
	}

	// Dangerous protocol detection
	err = ValidateMemoryContent(limits, "XSS", "Click [here](javascript:alert(1))", nil, nil)
	if err == nil {
		t.Fatalf("expected error for javascript: link")
	}

	// Embedded URL credentials in source_refs
	err = ValidateMemoryContent(limits, "Ref", "Normal body", nil, []SourceRef{
		{Type: "url", Ref: "https://admin:password123@example.com/internal"},
	})
	if err == nil {
		t.Fatalf("expected error for embedded credentials in source_ref URL")
	}
}
