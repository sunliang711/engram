package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSchedulerPreviewHandler validates unsaved plans without requiring a database or task execution.
func TestSchedulerPreviewHandler(t *testing.T) {
	h := &Handler{}
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"schedule_type":"daily","cron_expression":"0 4 * * *","timezone":"Asia/Singapore"}`, http.StatusOK},
		{`{"schedule_type":"interval","interval":"30m"}`, http.StatusOK},
		{`{"schedule_type":"cron","cron_expression":"bad"}`, http.StatusBadRequest},
		{`{"cron_expression":"0 4 * * *","timezone":"bad/zone"}`, http.StatusBadRequest},
		{`{`, http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		h.handleSchedulerPreview(rec, httptest.NewRequest(http.MethodPost, "/api/admin/scheduler/preview", strings.NewReader(tc.body)))
		if rec.Code != tc.want {
			t.Fatalf("preview %s: %d %s", tc.body, rec.Code, rec.Body.String())
		}
		if tc.want == http.StatusOK {
			var result struct {
				NextRuns []time.Time `json:"next_runs"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || len(result.NextRuns) != 5 {
				t.Fatalf("preview dates=%v err=%v", result.NextRuns, err)
			}
		}
	}
	rec := httptest.NewRecorder()
	h.handleSchedulerPreview(rec, httptest.NewRequest(http.MethodGet, "/api/admin/scheduler/preview", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET preview status=%d", rec.Code)
	}
}
