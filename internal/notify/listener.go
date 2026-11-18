package notify

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"ai-memory/internal/auth"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

const ChannelName = "memory_events"

// EventPayload is the internal JSON payload sent via PostgreSQL pg_notify.
type EventPayload struct {
	TenantID      uuid.UUID  `json:"tenant_id"`
	ProjectID     *uuid.UUID `json:"project_id,omitempty"`
	EventRevision int64      `json:"event_revision"`
	CommittedAt   time.Time  `json:"committed_at"`
}

// ClientNotice is the minimal notification delivered to subscribed online clients (Section 10).
type ClientNotice struct {
	LatestCursor string `json:"latest_cursor"`
}

type subscriber struct {
	id        uint64
	tenantID  uuid.UUID
	projectID *uuid.UUID
	ch        chan ClientNotice
}

// Hub manages PostgreSQL LISTEN/NOTIFY and dispatches minimal change notices to subscribed clients.
type Hub struct {
	pool   *pgxpool.Pool
	signer *auth.TokenSigner
	log    zerolog.Logger

	mu     sync.RWMutex
	nextID uint64
	subs   map[uint64]*subscriber
}

func NewHub(pool *pgxpool.Pool, signer *auth.TokenSigner, log zerolog.Logger) *Hub {
	return &Hub{
		pool:   pool,
		signer: signer,
		log:    log,
		subs:   make(map[uint64]*subscriber),
	}
}

// Subscribe registers an online client session to receive change notifications for its authorized scope.
func (h *Hub) Subscribe(tenantID uuid.UUID, projectID *uuid.UUID) (<-chan ClientNotice, func()) {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	ch := make(chan ClientNotice, 16)
	h.subs[id] = &subscriber{
		id:        id,
		tenantID:  tenantID,
		projectID: projectID,
		ch:        ch,
	}
	h.mu.Unlock()

	unsubscribe := func() {
		h.mu.Lock()
		if sub, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(sub.ch)
		}
		h.mu.Unlock()
	}
	return ch, unsubscribe
}

// PublishLocal dispatches an EventPayload to local subscribers (called both from LISTEN and local fallback).
func (h *Hub) PublishLocal(ev EventPayload) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, sub := range h.subs {
		if sub.tenantID != ev.TenantID {
			continue
		}
		// Global events (ev.ProjectID == nil) apply to all subscribers in the tenant.
		// Project events apply only to subscribers bound to that specific project.
		if ev.ProjectID != nil {
			if sub.projectID == nil || *sub.projectID != *ev.ProjectID {
				continue
			}
		}
		cursor, err := h.signer.SignCursor(sub.tenantID, sub.projectID, ev.EventRevision)
		if err != nil {
			continue
		}
		select {
		case sub.ch <- ClientNotice{LatestCursor: cursor}:
		default:
			// Non-blocking: if client channel is full, drop older notice and enqueue latest
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- ClientNotice{LatestCursor: cursor}:
			default:
			}
		}
	}
}

// RunListenLoop connects to PostgreSQL and listens on ChannelName until ctx is cancelled.
func (h *Hub) RunListenLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := h.listenOnce(ctx); err != nil && ctx.Err() == nil {
			h.log.Warn().Err(err).Msg("postgres listen loop interrupted, retrying")
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (h *Hub) listenOnce(ctx context.Context) error {
	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+ChannelName); err != nil {
		return err
	}

	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var ev EventPayload
		if err := json.Unmarshal([]byte(notification.Payload), &ev); err != nil {
			h.log.Warn().Err(err).Msg("failed to unmarshal pg_notify payload")
			continue
		}
		h.PublishLocal(ev)
	}
}
