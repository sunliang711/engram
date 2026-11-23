package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"engram/internal/memory"
)

func TestHandleMemoryIndexSettings_ValidationAndDefaults(t *testing.T) {
	// nil memSvc GET returns built-in defaults cleanly.
	hNil := &Handler{}
	rec := httptest.NewRecorder()
	hNil.handleMemoryIndexSettings(rec, httptest.NewRequest(http.MethodGet, "/api/admin/settings/memory-index", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for GET with nil memSvc, got %d: %s", rec.Code, rec.Body.String())
	}
	var defaults memory.IndexSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &defaults); err != nil {
		t.Fatalf("decode defaults: %v", err)
	}
	if defaults != memory.DefaultIndexSettings() {
		t.Fatalf("expected defaults %+v, got %+v", memory.DefaultIndexSettings(), defaults)
	}

	h := &Handler{memSvc: &memory.Service{}}

	// Unknown JSON field must be rejected with 400.
	recUnknown := httptest.NewRecorder()
	reqUnknown := httptest.NewRequest(http.MethodPut, "/api/admin/settings/memory-index", bytes.NewBufferString(`{"unknown_field": 10}`))
	h.handleMemoryIndexSettings(recUnknown, reqUnknown)
	if recUnknown.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for unknown field, got %d: %s", recUnknown.Code, recUnknown.Body.String())
	}

	// Empty payload must be rejected with 400.
	recEmpty := httptest.NewRecorder()
	reqEmpty := httptest.NewRequest(http.MethodPut, "/api/admin/settings/memory-index", bytes.NewBufferString(`{}`))
	h.handleMemoryIndexSettings(recEmpty, reqEmpty)
	if recEmpty.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for empty update, got %d: %s", recEmpty.Code, recEmpty.Body.String())
	}

	// Unsupported HTTP method must return 405.
	recMethod := httptest.NewRecorder()
	h.handleMemoryIndexSettings(recMethod, httptest.NewRequest(http.MethodDelete, "/api/admin/settings/memory-index", nil))
	if recMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 Method Not Allowed for DELETE, got %d", recMethod.Code)
	}
}
