package admin

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func sampleSnapshotPayload() SnapshotPayload {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	tid := uuid.New()
	pid := uuid.New()
	projID := uuid.New()
	marker := uuid.New()
	memID := uuid.New()
	tokID := uuid.New()
	return SnapshotPayload{
		Tenants: []snapshotTenant{
			{ID: tid, Name: "default", Status: "active", CreatedAt: now},
		},
		Principals: []snapshotPrincipal{
			{ID: pid, IdentityProvider: "api_token", Subject: "macbook", DisplayName: "MacBook", Status: "active", CreatedAt: now},
		},
		TenantMembers: []snapshotTenantMember{
			{TenantID: tid, PrincipalID: pid, Role: "admin", CreatedAt: now},
		},
		Projects: []snapshotProject{
			{ID: projID, TenantID: tid, Name: "broker-core", Marker: &marker, Status: "active", CreatedAt: now, UpdatedAt: now},
		},
		ProjectMembers: []snapshotProjectMember{
			{ProjectID: projID, PrincipalID: pid, Role: "maintainer", CreatedAt: now},
		},
		ProjectRepositories: []snapshotProjectRepository{
			{ID: uuid.New(), TenantID: tid, ProjectID: projID, CanonicalRemote: "github.com/eagle/broker-core", WorkspacePath: "", IsPrimary: true, CreatedAt: now},
		},
		APITokens: []snapshotAPIToken{
			{ID: tokID, TokenHash: "hash123", TokenPrefix: "aim_12345678", TokenCiphertext: "v1:cipher", RawToken: "aim_secret_token", TenantID: tid, PrincipalID: pid, Name: "default", CreatedAt: now},
		},
		Memories: []snapshotMemory{
			{
				ID:          memID,
				TenantID:    tid,
				Scope:       "project",
				ProjectID:   &projID,
				Category:    "env",
				Title:       "UAT Address",
				ContentMD:   "https://uat.example.com",
				Tags:        []string{"uat"},
				SourceRefs:  json.RawMessage(`[]`),
				IsPinned:    true,
				Status:      "active",
				Version:     1,
				ContentHash: "memhash",
				CreatedBy:   pid,
				UpdatedBy:   pid,
				CreatedAt:   now,
				UpdatedAt:   now,
			},
		},
		MemoryRevisions: []snapshotMemoryRevision{
			{ID: 1, TenantID: tid, MemoryID: memID, Version: 1, Operation: "create", Snapshot: json.RawMessage(`{"title":"UAT Address"}`), ActorID: pid, RequestID: "req_1", CreatedAt: now},
		},
		MemoryEvents: []snapshotMemoryEvent{
			{Revision: 1, TenantID: tid, ProjectID: &projID, MemoryID: memID, Operation: "create", MemoryVersion: 1, ActorID: pid, CreatedAt: now},
		},
	}
}

func TestSnapshotArchive_UnencryptedRoundTrip(t *testing.T) {
	payload := sampleSnapshotPayload()
	payload.APITokens[0].RawToken = ""

	zipBytes, manifest, err := EncodeSnapshotArchive(payload, "", time.Now())
	if err != nil {
		t.Fatalf("EncodeSnapshotArchive failed: %v", err)
	}
	if manifest.Encrypted {
		t.Fatal("expected manifest.Encrypted=false")
	}

	gotManifest, gotPayload, err := DecodeSnapshotArchive(zipBytes, "")
	if err != nil {
		t.Fatalf("DecodeSnapshotArchive failed: %v", err)
	}
	if gotManifest.PayloadSHA256 != manifest.PayloadSHA256 {
		t.Fatalf("sha mismatch: got %q want %q", gotManifest.PayloadSHA256, manifest.PayloadSHA256)
	}
	if len(gotPayload.Memories) != 1 || gotPayload.Memories[0].Title != "UAT Address" {
		t.Fatalf("unexpected decoded memories: %+v", gotPayload.Memories)
	}
}

func TestSnapshotArchive_EncryptedRoundTripAndErrors(t *testing.T) {
	payload := sampleSnapshotPayload()
	zipBytes, manifest, err := EncodeSnapshotArchive(payload, "correct-horse-battery-staple", time.Now())
	if err != nil {
		t.Fatalf("EncodeSnapshotArchive encrypted failed: %v", err)
	}
	if !manifest.Encrypted {
		t.Fatal("expected manifest.Encrypted=true")
	}

	// Ensure payload.json is NOT present in encrypted zip.
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name == "payload.json" {
			t.Fatal("encrypted snapshot archive must not contain plaintext payload.json")
		}
	}

	// Missing passphrase must fail.
	if _, _, err := DecodeSnapshotArchive(zipBytes, ""); err == nil {
		t.Fatal("expected error when passphrase is missing")
	}
	// Wrong passphrase must fail.
	if _, _, err := DecodeSnapshotArchive(zipBytes, "wrong-passphrase"); err == nil {
		t.Fatal("expected error when passphrase is wrong")
	}

	// Correct passphrase restores full payload including RawToken.
	_, gotPayload, err := DecodeSnapshotArchive(zipBytes, "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("DecodeSnapshotArchive with right passphrase failed: %v", err)
	}
	if len(gotPayload.APITokens) != 1 || gotPayload.APITokens[0].RawToken != "aim_secret_token" {
		t.Fatalf("expected raw_token to be preserved inside encrypted snapshot, got %+v", gotPayload.APITokens)
	}
}
