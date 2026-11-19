package notify

import (
	"io"
	"testing"
	"time"

	"engram/internal/auth"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// TestScopedNotifications prevents global and other-project notices reaching project-only clients.
func TestScopedNotifications(t *testing.T) {
	tenant, project, other := uuid.New(), uuid.New(), uuid.New()
	hub := NewHub(nil, auth.NewTokenSigner("notification-test-signing-key", time.Hour), zerolog.New(io.Discard))
	projectOnly, cancelProject := hub.Subscribe(tenant, &project, false)
	defer cancelProject()
	globalOnly, cancelGlobal := hub.Subscribe(tenant, nil, true)
	defer cancelGlobal()
	both, cancelBoth := hub.Subscribe(tenant, &project, true)
	defer cancelBoth()
	for _, tc := range []struct {
		name                  string
		event                 EventPayload
		project, global, both bool
	}{
		{"global", EventPayload{TenantID: tenant, EventRevision: 1}, false, true, true},
		{"project", EventPayload{TenantID: tenant, ProjectID: &project, EventRevision: 2}, true, false, true},
		{"other project", EventPayload{TenantID: tenant, ProjectID: &other, EventRevision: 3}, false, false, false},
		{"other tenant", EventPayload{TenantID: uuid.New(), EventRevision: 4}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub.PublishLocal(tc.event)
			for _, check := range []struct {
				ch   <-chan ClientNotice
				want bool
			}{{projectOnly, tc.project}, {globalOnly, tc.global}, {both, tc.both}} {
				got := false
				select {
				case <-check.ch:
					got = true
				default:
				}
				if got != check.want {
					t.Fatalf("received=%v, want=%v", got, check.want)
				}
			}
		})
	}
}
