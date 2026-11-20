package observability

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type toolStat struct {
	calls       atomic.Int64
	errors      atomic.Int64
	totalMicros atomic.Int64
}

// Metrics collects operational metrics for the Memory Server.
type Metrics struct {
	mu                 sync.RWMutex
	toolStats          map[string]*toolStat
	versionConflicts   atomic.Int64
	idempotencyHits    atomic.Int64
	searchQueries      atomic.Int64
	searchHits         atomic.Int64
	eventsPublished    atomic.Int64
	lastEventLagMillis atomic.Int64
	exportBacklog      atomic.Int64
	lastExportUnix     atomic.Int64
	auditEventsTotal   atomic.Int64
	auditCleanupTotal  atomic.Int64
	schedulerRunsTotal atomic.Int64
	schedulerErrsTotal atomic.Int64
}

func NewMetrics() *Metrics {
	return &Metrics{
		toolStats: make(map[string]*toolStat),
	}
}

func (m *Metrics) getToolStat(tool string) *toolStat {
	m.mu.RLock()
	st, ok := m.toolStats[tool]
	m.mu.RUnlock()
	if ok {
		return st
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok = m.toolStats[tool]; ok {
		return st
	}
	st = &toolStat{}
	m.toolStats[tool] = st
	return st
}

func (m *Metrics) ObserveToolCall(tool string, d time.Duration, isErr bool) {
	st := m.getToolStat(tool)
	st.calls.Add(1)
	if isErr {
		st.errors.Add(1)
	}
	st.totalMicros.Add(d.Microseconds())
}

func (m *Metrics) IncVersionConflict() {
	m.versionConflicts.Add(1)
}

func (m *Metrics) IncIdempotencyHit() {
	m.idempotencyHits.Add(1)
}

func (m *Metrics) ObserveSearch(resultCount int) {
	m.searchQueries.Add(1)
	if resultCount > 0 {
		m.searchHits.Add(1)
	}
}

func (m *Metrics) ObserveEventLag(d time.Duration) {
	m.eventsPublished.Add(1)
	m.exportBacklog.Add(1)
	m.lastEventLagMillis.Store(d.Milliseconds())
}

func (m *Metrics) SetExportBacklog(backlog int64) {
	m.exportBacklog.Store(backlog)
	m.lastExportUnix.Store(time.Now().UTC().Unix())
}

func (m *Metrics) IncAuditEvent() {
	m.auditEventsTotal.Add(1)
}

func (m *Metrics) AddAuditCleanupDeleted(n int64) {
	if n > 0 {
		m.auditCleanupTotal.Add(n)
	}
}

func (m *Metrics) ObserveSchedulerRun(isErr bool) {
	m.schedulerRunsTotal.Add(1)
	if isErr {
		m.schedulerErrsTotal.Add(1)
	}
}

// RenderPrometheus outputs metrics in Prometheus text exposition format.
func (m *Metrics) RenderPrometheus(pool *pgxpool.Pool) string {
	var b strings.Builder

	m.mu.RLock()
	tools := make([]string, 0, len(m.toolStats))
	for k := range m.toolStats {
		tools = append(tools, k)
	}
	sort.Strings(tools)
	for _, t := range tools {
		st := m.toolStats[t]
		fmt.Fprintf(&b, "memory_tool_calls_total{tool=%q} %d\n", t, st.calls.Load())
		fmt.Fprintf(&b, "memory_tool_errors_total{tool=%q} %d\n", t, st.errors.Load())
		fmt.Fprintf(&b, "memory_tool_duration_microseconds_sum{tool=%q} %d\n", t, st.totalMicros.Load())
	}
	m.mu.RUnlock()

	fmt.Fprintf(&b, "memory_version_conflicts_total %d\n", m.versionConflicts.Load())
	fmt.Fprintf(&b, "memory_idempotency_hits_total %d\n", m.idempotencyHits.Load())
	fmt.Fprintf(&b, "memory_search_queries_total %d\n", m.searchQueries.Load())
	fmt.Fprintf(&b, "memory_search_hits_total %d\n", m.searchHits.Load())
	fmt.Fprintf(&b, "memory_events_published_total %d\n", m.eventsPublished.Load())
	fmt.Fprintf(&b, "memory_event_lag_milliseconds %d\n", m.lastEventLagMillis.Load())
	fmt.Fprintf(&b, "memory_export_backlog_count %d\n", m.exportBacklog.Load())
	fmt.Fprintf(&b, "memory_export_last_timestamp_seconds %d\n", m.lastExportUnix.Load())
	fmt.Fprintf(&b, "engram_audit_events_total %d\n", m.auditEventsTotal.Load())
	fmt.Fprintf(&b, "engram_audit_cleanup_deleted_total %d\n", m.auditCleanupTotal.Load())
	fmt.Fprintf(&b, "engram_scheduler_runs_total %d\n", m.schedulerRunsTotal.Load())
	fmt.Fprintf(&b, "engram_scheduler_errors_total %d\n", m.schedulerErrsTotal.Load())

	if pool != nil {
		stat := pool.Stat()
		fmt.Fprintf(&b, "memory_db_pool_acquired_conns %d\n", stat.AcquiredConns())
		fmt.Fprintf(&b, "memory_db_pool_idle_conns %d\n", stat.IdleConns())
		fmt.Fprintf(&b, "memory_db_pool_total_conns %d\n", stat.TotalConns())
		fmt.Fprintf(&b, "memory_db_pool_max_conns %d\n", stat.MaxConns())
	}

	return b.String()
}
