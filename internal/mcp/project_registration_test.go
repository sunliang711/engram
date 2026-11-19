package mcp

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"engram/internal/auth"

	"github.com/rs/zerolog"
)

// TestProjectRegisterRejectsAuthorityArguments prevents clients from choosing tenants or their own grants.
func TestProjectRegisterRejectsAuthorityArguments(t *testing.T) {
	srv := &Server{log: zerolog.New(io.Discard)}
	for _, raw := range []string{`{"tenant_id":"other"}`, `{"principal_id":"other"}`, `{"role":"writer"}`, `{"can_create_project":true}`} {
		result := srv.dispatchToolCall(context.Background(), auth.Principal{}, "project_register", json.RawMessage(raw), "registration")
		if !result.IsError {
			t.Fatalf("accepted authority input: %s", raw)
		}
		var errBody struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].Text), &errBody); err != nil || errBody.Code != "INVALID_ARGUMENT" {
			t.Fatalf("invalid validation response: %+v, %v", errBody, err)
		}
	}
}
