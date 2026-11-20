package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"engram/internal/apperror"
	"engram/internal/audit"
	"engram/internal/auth"
	"engram/internal/db"
	"engram/internal/memory"
	"engram/internal/notify"
	"engram/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const ProtocolVersion = "2025-03-26"

// Server implements the MCP Streamable HTTP transport and operational endpoints.
type Server struct {
	db       *db.Database
	auth     *auth.Authenticator
	signer   *auth.TokenSigner
	service  *memory.Service
	hub      *notify.Hub
	metrics  *observability.Metrics
	auditSvc *audit.Service
	log      zerolog.Logger
	mux      *http.ServeMux
}

// SetAuditService attaches an audit service to record MCP tool calls and auth failures.
func (s *Server) SetAuditService(svc *audit.Service) {
	s.auditSvc = svc
}

type clientIPCtxKey struct{}

func withClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPCtxKey{}, ip)
}

func clientIPFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(clientIPCtxKey{}).(string); ok {
		return v
	}
	return ""
}

func extractClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func NewServer(
	database *db.Database,
	authenticator *auth.Authenticator,
	signer *auth.TokenSigner,
	service *memory.Service,
	hub *notify.Hub,
	metrics *observability.Metrics,
	log zerolog.Logger,
) *Server {
	s := &Server{
		db:      database,
		auth:    authenticator,
		signer:  signer,
		service: service,
		hub:     hub,
		metrics: metrics,
		log:     log,
		mux:     http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) Mux() *http.ServeMux {
	return s.mux
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/readyz", s.handleReadyz)
	s.mux.HandleFunc("/metrics", s.handleMetrics)
	s.mux.HandleFunc("/mcp", s.handleMCP)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	if err := s.db.Ping(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ready"}`))
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(s.metrics.RenderPrometheus(s.db.Pool)))
}

// JSON-RPC 2.0 structures
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolCallResult struct {
	Content []toolContentItem `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

type toolContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	reqID := strings.TrimSpace(r.Header.Get("X-Request-Id"))
	if reqID == "" {
		reqID = "req_" + uuid.NewString()
	}
	w.Header().Set("X-Request-Id", reqID)
	clientIP := extractClientIP(r)

	principal, err := s.auth.AuthenticateRequest(r.Context(), r)
	if err != nil {
		appErr := apperror.AsError(err, reqID)
		s.auditSvc.Record(r.Context(), audit.Entry{
			ActorType:    audit.ActorAnonymous,
			Source:       audit.SourceMCP,
			Action:       "mcp.auth",
			ResourceType: "mcp",
			Status:       audit.StatusDenied,
			ErrorCode:    string(appErr.Code),
			ErrorMessage: appErr.Message,
			RequestID:    reqID,
			ClientIP:     clientIP,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(appErr.HTTPStatus())
		_ = json.NewEncoder(w).Encode(appErr)
		return
	}
	ctx := auth.WithPrincipal(withClientIP(r.Context(), clientIP), principal)

	switch r.Method {
	case http.MethodPost:
		s.handleMCPPost(ctx, w, r, principal, reqID)
	case http.MethodGet:
		s.handleMCPStream(ctx, w, r, principal, reqID)
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMCPPost(ctx context.Context, w http.ResponseWriter, r *http.Request, principal auth.Principal, reqID string) {
	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	if sessionID == "" {
		sessionID = "sess_" + uuid.NewString()
	}
	w.Header().Set("Mcp-Session-Id", sessionID)

	body, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
	if err != nil {
		writeJSONRPCError(w, nil, -32700, "Failed to read request body", apperror.New(apperror.CodeInvalidArgument, "failed to read body").WithRequestID(reqID))
		return
	}

	var rpcReq jsonRPCRequest
	if err := json.Unmarshal(body, &rpcReq); err != nil || rpcReq.JSONRPC != "2.0" || rpcReq.Method == "" {
		writeJSONRPCError(w, rpcReq.ID, -32600, "Invalid JSON-RPC 2.0 request", apperror.New(apperror.CodeInvalidArgument, "invalid json-rpc request").WithRequestID(reqID))
		return
	}

	// JSON-RPC notifications (no ID) return 202 Accepted
	if len(rpcReq.ID) == 0 || string(rpcReq.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var resp jsonRPCResponse
	resp.JSONRPC = "2.0"
	resp.ID = rpcReq.ID

	switch rpcReq.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{
					"listChanged": false,
				},
			},
			"serverInfo": map[string]any{
				"name":    "engram-server",
				"version": "1.0.0",
			},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{
			"tools": toolDefinitions(),
		}
	case "tools/call":
		var params toolCallParams
		if err := json.Unmarshal(rpcReq.Params, &params); err != nil || params.Name == "" {
			resp.Error = &jsonRPCError{
				Code:    -32602,
				Message: "Invalid params for tools/call",
				Data:    apperror.New(apperror.CodeInvalidArgument, "invalid tools/call params").WithRequestID(reqID),
			}
		} else {
			resp.Result = s.dispatchToolCall(ctx, principal, params.Name, params.Arguments, reqID)
		}
	default:
		resp.Error = &jsonRPCError{
			Code:    -32601,
			Message: "Method not found: " + rpcReq.Method,
		}
	}

	// Streamable HTTP: support SSE response if explicitly requested and JSON is not preferred
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "text/event-stream") && !strings.Contains(accept, "application/json") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		b, _ := json.Marshal(resp)
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(b))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleMCPStream limits notifications to authorized scopes and rechecks live grants.
func (s *Server) handleMCPStream(ctx context.Context, w http.ResponseWriter, r *http.Request, principal auth.Principal, reqID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	var projID *uuid.UUID
	ctxToken := strings.TrimSpace(r.URL.Query().Get("project_context"))
	if ctxToken == "" {
		ctxToken = strings.TrimSpace(r.Header.Get("X-Project-Context"))
	}
	if ctxToken != "" {
		claims, err := s.signer.VerifyProjectContext(ctxToken, principal.TenantID, principal.PrincipalID)
		if err != nil {
			appErr := apperror.AsError(err, reqID)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(appErr.HTTPStatus())
			_ = json.NewEncoder(w).Encode(appErr)
			return
		}
		if claims.Mode == auth.ModeProject && claims.ProjectID != nil {
			role, err := s.auth.GetProjectRole(ctx, principal.TenantID, *claims.ProjectID, principal.PrincipalID)
			if err != nil || !auth.CanReadProject(role) {
				appErr := apperror.New(apperror.CodeUnauthorized, "unauthorized project stream").WithRequestID(reqID)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(appErr.HTTPStatus())
				_ = json.NewEncoder(w).Encode(appErr)
				return
			}
			projID = claims.ProjectID
		}
	}

	includeGlobal := auth.CanReadGlobal(principal.TenantRole)
	if projID == nil && !includeGlobal {
		appErr := apperror.New(apperror.CodeUnauthorized, "no readable stream scope").WithRequestID(reqID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(appErr.HTTPStatus())
		_ = json.NewEncoder(w).Encode(appErr)
		return
	}
	// Recheck the original credentials as well as grants so revoked or expired tokens close existing streams.
	stillAuthorized := func() bool {
		p, err := s.auth.AuthenticateRequest(ctx, r)
		if err != nil || p.TenantID != principal.TenantID || p.PrincipalID != principal.PrincipalID || (includeGlobal != auth.CanReadGlobal(p.TenantRole)) {
			return false
		}
		if projID != nil {
			role, err := s.auth.GetProjectRole(ctx, p.TenantID, *projID, p.PrincipalID)
			return err == nil && auth.CanReadProject(role)
		}
		return true
	}

	// Disable the global server WriteTimeout for long-lived SSE streams
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, unsubscribe := s.hub.Subscribe(principal.TenantID, projID, includeGlobal)
	defer unsubscribe()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			if !stillAuthorized() {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
			_ = rc.SetWriteDeadline(time.Time{})
		case notice, ok := <-ch:
			if !ok || !stillAuthorized() {
				return
			}
			rpcNotice := map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/memory/changed",
				"params":  notice,
			}
			b, err := json.Marshal(rpcNotice)
			if err != nil {
				continue
			}
			_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(b)); err != nil {
				return
			}
			flusher.Flush()
			_ = rc.SetWriteDeadline(time.Time{})
		}
	}
}

// dispatchToolCall validates arguments and dispatches authenticated MCP operations.
func (s *Server) dispatchToolCall(ctx context.Context, principal auth.Principal, toolName string, rawArgs json.RawMessage, reqID string) toolCallResult {
	start := time.Now()
	var (
		res any
		err error
	)

	switch toolName {
	case "project_register":
		var req memory.RegisterProjectRequest
		if err = memory.DecodeStrictJSON(rawArgs, &req); err == nil {
			res, err = s.service.RegisterProject(ctx, req)
		}
	case "memory_discover":
		var req memory.DiscoverRequest
		if err = memory.DecodeStrictJSON(rawArgs, &req); err == nil {
			res, err = s.service.Discover(ctx, req)
		}
	case "memory_query":
		var req memory.QueryRequest
		if err = memory.DecodeStrictJSON(rawArgs, &req); err == nil {
			res, err = s.service.Query(ctx, req)
		}
	case "memory_read":
		var req memory.ReadRequest
		if err = memory.DecodeStrictJSON(rawArgs, &req); err == nil {
			res, err = s.service.Read(ctx, req)
		}
	case "memory_create":
		var req memory.CreateRequest
		if err = memory.DecodeStrictJSON(rawArgs, &req); err == nil {
			res, err = s.service.Create(ctx, req, reqID)
		}
	case "memory_update":
		var req memory.UpdateRequest
		if err = memory.DecodeStrictJSON(rawArgs, &req); err == nil {
			res, err = s.service.Update(ctx, req, reqID)
		}
	case "memory_delete":
		var req memory.DeleteRequest
		if err = memory.DecodeStrictJSON(rawArgs, &req); err == nil {
			res, err = s.service.Delete(ctx, req, reqID)
		}
	case "memory_changes":
		var req memory.ChangesRequest
		if err = memory.DecodeStrictJSON(rawArgs, &req); err == nil {
			res, err = s.service.Changes(ctx, req)
		}
	case "memory_promote":
		var req memory.PromoteRequest
		if err = memory.DecodeStrictJSON(rawArgs, &req); err == nil {
			res, err = s.service.Promote(ctx, req, reqID)
		}
	default:
		err = apperror.New(apperror.CodeInvalidArgument, "unknown tool: "+toolName)
	}

	duration := time.Since(start)
	if s.metrics != nil {
		s.metrics.ObserveToolCall(toolName, duration, err != nil)
	}

	s.recordToolAudit(ctx, principal, toolName, rawArgs, res, err, reqID, duration)

	if err != nil {
		appErr := apperror.AsError(err, reqID)
		s.log.Info().
			Str("request_id", reqID).
			Str("tenant_id", principal.TenantID.String()).
			Str("principal_id", principal.PrincipalID.String()).
			Str("tool", toolName).
			Str("error_code", string(appErr.Code)).
			Bool("retryable", appErr.Retryable).
			Dur("duration_ms", duration).
			Msg("mcp tool call failed")

		errJSON, _ := json.Marshal(appErr)
		return toolCallResult{
			Content: []toolContentItem{
				{Type: "text", Text: string(errJSON)},
			},
			IsError: true,
		}
	}

	s.log.Info().
		Str("request_id", reqID).
		Str("tenant_id", principal.TenantID.String()).
		Str("principal_id", principal.PrincipalID.String()).
		Str("tool", toolName).
		Dur("duration_ms", duration).
		Msg("mcp tool call succeeded")

	resJSON, _ := json.Marshal(res)
	return toolCallResult{
		Content: []toolContentItem{
			{Type: "text", Text: string(resJSON)},
		},
		IsError: false,
	}
}

func isReadOnlyTool(toolName string) bool {
	switch toolName {
	case "memory_discover", "memory_query", "memory_read", "memory_changes":
		return true
	default:
		return false
	}
}

func (s *Server) recordToolAudit(ctx context.Context, principal auth.Principal, toolName string, rawArgs json.RawMessage, res any, callErr error, reqID string, duration time.Duration) {
	if s.auditSvc == nil || !s.auditSvc.Enabled() {
		return
	}
	if callErr == nil && isReadOnlyTool(toolName) && !s.auditSvc.LogReads() {
		return
	}

	var argPeek struct {
		AgentName string `json:"agent_name"`
		MemoryID  string `json:"memory_id"`
		Scope     string `json:"scope"`
		Category  string `json:"category"`
		Title     string `json:"title"`
		Query     string `json:"query"`
		Name      string `json:"name"`
	}
	if len(rawArgs) > 0 {
		_ = json.Unmarshal(rawArgs, &argPeek)
	}

	tenantID := principal.TenantID
	entry := audit.Entry{
		TenantID:   &tenantID,
		ActorType:  audit.ActorPrincipal,
		ActorID:    principal.PrincipalID.String(),
		ActorName:  principal.DisplayName,
		AgentName:  strings.TrimSpace(argPeek.AgentName),
		Source:     audit.SourceMCP,
		Action:     "mcp." + toolName,
		Status:     audit.StatusSuccess,
		RequestID:  reqID,
		ClientIP:   clientIPFromContext(ctx),
		DurationMS: duration.Milliseconds(),
		Details:    map[string]any{},
	}

	switch toolName {
	case "project_register":
		entry.ResourceType = "project"
		if argPeek.Name != "" {
			entry.Details["project_name"] = argPeek.Name
		}
	case "memory_discover":
		entry.ResourceType = "project"
	case "memory_query", "memory_changes":
		entry.ResourceType = "memory"
		if argPeek.Query != "" {
			entry.Details["query"] = argPeek.Query
		}
	case "memory_read", "memory_create", "memory_update", "memory_delete", "memory_promote":
		entry.ResourceType = "memory"
		if argPeek.MemoryID != "" {
			entry.ResourceID = argPeek.MemoryID
		}
		if argPeek.Scope != "" {
			entry.Details["scope"] = argPeek.Scope
		}
		if argPeek.Category != "" {
			entry.Details["category"] = argPeek.Category
		}
		if argPeek.Title != "" {
			entry.Details["title"] = argPeek.Title
		}
	}

	switch r := res.(type) {
	case *memory.DiscoverResponse:
		if r != nil {
			entry.Details["mode"] = r.Mode
			if r.Project != nil {
				pid := r.Project.ID
				entry.ProjectID = &pid
				entry.ResourceID = pid.String()
				entry.Details["project_name"] = r.Project.Name
			}
		}
	case *memory.RegisterProjectResponse:
		if r != nil {
			pid := r.Project.ID
			entry.ProjectID = &pid
			entry.ResourceID = pid.String()
			entry.Details["project_name"] = r.Project.Name
		}
	case *memory.QueryResponse:
		if r != nil {
			entry.Details["result_count"] = len(r.Items)
		}
	case *memory.ReadResponse:
		if r != nil {
			entry.ResourceID = r.Memory.ID.String()
			entry.ProjectID = r.Memory.ProjectID
			entry.Details["scope"] = r.Memory.Scope
			entry.Details["version"] = r.Memory.Version
			entry.Details["title"] = r.Memory.Title
		}
	case *memory.WriteMemoryResponse:
		if r != nil {
			entry.ResourceID = r.Memory.ID.String()
			entry.ProjectID = r.Memory.ProjectID
			entry.Details["scope"] = r.Memory.Scope
			entry.Details["category"] = r.Memory.Category
			entry.Details["version"] = r.Memory.Version
			entry.Details["title"] = r.Memory.Title
		}
	case *memory.DeleteResponse:
		if r != nil {
			entry.ResourceID = r.MemoryID.String()
			entry.Details["version"] = r.Version
		}
	case *memory.ChangesResponse:
		if r != nil {
			entry.Details["event_count"] = len(r.Events)
		}
	}

	if callErr != nil {
		appErr := apperror.AsError(callErr, reqID)
		entry.ErrorCode = string(appErr.Code)
		entry.ErrorMessage = appErr.Message
		if appErr.Code == apperror.CodeUnauthorized || appErr.Code == apperror.CodeUnauthenticated || appErr.Code == apperror.CodeRateLimited {
			entry.Status = audit.StatusDenied
		} else {
			entry.Status = audit.StatusFailed
		}
	}

	s.auditSvc.Record(ctx, entry)
}

func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &jsonRPCError{
			Code:    code,
			Message: msg,
			Data:    data,
		},
	})
}

// toolDefinitions publishes the supported memory and project registration schemas.
func toolDefinitions() []map[string]any {
	return []map[string]any{
		projectRegisterDefinition(),
		{
			"name":        "memory_discover",
			"description": "Resolve current repository/project identity and return project_context, permissions, compact scope indices, and latest_cursor. Pass Git remotes (e.g. git remote -v) and/or project_marker (.engram-project UUID) to identify a project, or set allow_global_only=true when outside a registered project.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_marker": map[string]any{
						"type":        "string",
						"description": "UUID read from the nearest .engram-project file, if present.",
					},
					"marker_path": map[string]any{
						"type":        "string",
						"description": "Directory containing .engram-project relative to the Git root (empty string if at Git root). Do not pass an absolute local filesystem path.",
					},
					"workspace_path": map[string]any{
						"type":        "string",
						"description": "Optional monorepo sub-directory relative to the Git root (e.g. 'services/api'; omit or pass '' at Git root). Must NOT be an absolute local path.",
					},
					"allow_global_only": map[string]any{
						"type":        "boolean",
						"description": "Set to true to allow global_only fallback (tenant global memories only) when no project identity is provided or the project is unregistered.",
					},
					"remotes": map[string]any{
						"type":        "array",
						"description": "Git remotes of the current repository (e.g. origin). Required to identify a project unless project_marker is provided.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name": map[string]any{"type": "string", "description": "Git remote name (e.g. 'origin')."},
								"url":  map[string]any{"type": "string", "description": "Git remote URL (SSH or HTTPS, e.g. 'git@github.com:org/repo.git')."},
							},
							"required": []string{"name", "url"},
						},
					},
				},
			},
		},
		{
			"name":        "memory_query",
			"description": "Search or list memories across global and/or current project scopes.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_context": map[string]any{"type": "string", "description": "Token returned by memory_discover."},
					"scopes":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional scopes filter: 'global' and/or 'project'."},
					"query":           map[string]any{"type": "string", "description": "Search keywords (empty string lists memories by recency)."},
					"categories":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional category filter."},
					"tags":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional tag filter (matches items containing all specified tags)."},
					"status":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional status filter (defaults to ['active'])."},
					"cursor":          map[string]any{"type": "string", "description": "Pagination cursor from previous response's next_cursor."},
					"limit":           map[string]any{"type": "integer", "description": "Maximum number of items to return."},
				},
				"required": []string{"project_context"},
			},
		},
		{
			"name":        "memory_read",
			"description": "Read the full Markdown content and metadata of a specific memory (latest or historical version).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_context": map[string]any{"type": "string", "description": "Token returned by memory_discover."},
					"memory_id":       map[string]any{"type": "string", "description": "UUID of the memory to read."},
					"version":         map[string]any{"type": "integer", "description": "Optional historical version number to read."},
				},
				"required": []string{"project_context", "memory_id"},
			},
		},
		{
			"name":        "memory_create",
			"description": "Create a new project or global memory entry with idempotency protection.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{"type": "string", "maxLength": 128, "description": "Optional self-reported agent name, e.g. Codex, Claude Code, or review-agent. Omit for unknown. Keep the same name on idempotent retries; this metadata never grants permissions."},
					"project_context": map[string]any{"type": "string"},
					"scope":           map[string]any{"type": "string", "enum": []string{"project", "global"}},
					"category":        map[string]any{"type": "string"},
					"title":           map[string]any{"type": "string"},
					"content":         map[string]any{"type": "string"},
					"tags":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"is_pinned":       map[string]any{"type": "boolean"},
					"source_refs":     map[string]any{"type": "array"},
					"idempotency_key": map[string]any{"type": "string"},
				},
				"required": []string{"project_context", "scope", "category", "title", "content", "idempotency_key"},
			},
		},
		{
			"name":        "memory_update",
			"description": "Update an existing memory with optimistic locking (expected_version) and idempotency_key.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{"type": "string", "maxLength": 128, "description": "Optional self-reported agent name, e.g. Codex, Claude Code, or review-agent. Omit for unknown. Keep the same name on idempotent retries; this metadata never grants permissions."},
					"project_context":  map[string]any{"type": "string"},
					"memory_id":        map[string]any{"type": "string"},
					"expected_version": map[string]any{"type": "integer"},
					"patch":            map[string]any{"type": "object"},
					"idempotency_key":  map[string]any{"type": "string"},
				},
				"required": []string{"project_context", "memory_id", "expected_version", "patch", "idempotency_key"},
			},
		},
		{
			"name":        "memory_delete",
			"description": "Soft-delete a memory with optimistic locking (expected_version) and idempotency_key.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{"type": "string", "maxLength": 128, "description": "Optional self-reported agent name, e.g. Codex, Claude Code, or review-agent. Omit for unknown. Keep the same name on idempotent retries; this metadata never grants permissions."},
					"project_context":  map[string]any{"type": "string"},
					"memory_id":        map[string]any{"type": "string"},
					"expected_version": map[string]any{"type": "integer"},
					"idempotency_key":  map[string]any{"type": "string"},
				},
				"required": []string{"project_context", "memory_id", "expected_version", "idempotency_key"},
			},
		},
		{
			"name":        "memory_changes",
			"description": "Fetch incremental change events since cursor within the caller's authorized global and project scope.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_context": map[string]any{"type": "string"},
					"cursor":          map[string]any{"type": "string"},
					"limit":           map[string]any{"type": "integer"},
				},
				"required": []string{"project_context"},
			},
		},
		{
			"name":        "memory_promote",
			"description": "Promote a Project Memory to a Tenant Global Memory while retaining the original Project Memory.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{"type": "string", "maxLength": 128, "description": "Optional self-reported agent name, e.g. Codex, Claude Code, or review-agent. Omit for unknown. Keep the same name on idempotent retries; this metadata never grants permissions."},
					"project_context":  map[string]any{"type": "string"},
					"memory_id":        map[string]any{"type": "string"},
					"expected_version": map[string]any{"type": "integer"},
					"category":         map[string]any{"type": "string"},
					"title":            map[string]any{"type": "string"},
					"tags":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"is_pinned":        map[string]any{"type": "boolean"},
					"idempotency_key":  map[string]any{"type": "string"},
				},
				"required": []string{"project_context", "memory_id", "expected_version", "idempotency_key"},
			},
		},
	}
}
