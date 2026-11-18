package admin

import (
	"testing"
	"time"
)

func TestSealAndUnsealToken(t *testing.T) {
	mgr := NewAuthManager(nil, "persistent-signing-key-32bytes-minimum", time.Hour, false)
	raw := "aim_1234567890abcdef1234567890abcdef"

	sealed1, err := mgr.SealToken(raw)
	if err != nil {
		t.Fatalf("SealToken failed: %v", err)
	}
	sealed2, err := mgr.SealToken(raw)
	if err != nil {
		t.Fatalf("SealToken second call failed: %v", err)
	}
	if sealed1 == sealed2 {
		t.Fatal("expected random nonces to produce distinct ciphertexts")
	}

	got, err := mgr.UnsealToken(sealed1)
	if err != nil {
		t.Fatalf("UnsealToken failed: %v", err)
	}
	if got != raw {
		t.Fatalf("UnsealToken = %q, want %q", got, raw)
	}

	// Wrong key must fail cleanly.
	otherMgr := NewAuthManager(nil, "different-signing-key-32bytes-minimum", time.Hour, false)
	if _, err := otherMgr.UnsealToken(sealed1); err == nil {
		t.Fatal("expected UnsealToken with different key to fail")
	}

	// Invalid format must fail cleanly.
	if _, err := mgr.UnsealToken("not-valid"); err == nil {
		t.Fatal("expected invalid sealed token format to fail")
	}
}
