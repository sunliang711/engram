package safekeep

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"engram/internal/apperror"
)

// Client communicates with SafeKeep's Client Data Plane HTTP API (/api/v1/*).
type Client struct {
	testHTTP   *http.Client
	uploadHTTP *http.Client
}

// NewClient creates a SafeKeep HTTP client with appropriate timeouts.
func NewClient() *Client {
	return &Client{
		testHTTP:   &http.Client{Timeout: 15 * time.Second},
		uploadHTTP: &http.Client{Timeout: 5 * time.Minute},
	}
}

// ValidateBaseURL normalizes and validates a SafeKeep server base URL.
func ValidateBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", apperror.New(apperror.CodeInvalidArgument, "invalid safekeep base_url: "+err.Error())
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", apperror.New(apperror.CodeInvalidArgument, "safekeep base_url must use http or https")
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", apperror.New(apperror.CodeInvalidArgument, "safekeep base_url must include host")
	}
	if u.User != nil {
		return "", apperror.New(apperror.CodeInvalidArgument, "safekeep base_url must not contain embedded credentials")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// ListUpstreams queries GET /api/v1/upstreams to verify connectivity and discover allowed targets.
func (c *Client) ListUpstreams(ctx context.Context, baseURL, apiKey string) ([]UpstreamInfo, int64, error) {
	cleanURL, err := ValidateBaseURL(baseURL)
	if err != nil {
		return nil, 0, err
	}
	if cleanURL == "" {
		return nil, 0, apperror.New(apperror.CodeInvalidArgument, "safekeep base_url is required")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, 0, apperror.New(apperror.CodeInvalidArgument, "safekeep api_key is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cleanURL+"/api/v1/upstreams", nil)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInvalidArgument, "build safekeep request", err)
	}
	req.Header.Set("X-API-Key", apiKey)

	start := time.Now()
	resp, err := c.testHTTP.Do(req)
	latencyMS := time.Since(start).Milliseconds()
	if err != nil {
		return nil, latencyMS, apperror.Wrap(apperror.CodeInternalError, "connect to safekeep failed: "+err.Error(), err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, latencyMS, parseSafekeepHTTPError(resp.StatusCode, body)
	}

	var parsed struct {
		Items []UpstreamInfo `json:"items"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, latencyMS, apperror.Wrap(apperror.CodeInternalError, "decode safekeep upstreams response", err)
	}
	if parsed.Items == nil {
		parsed.Items = []UpstreamInfo{}
	}
	return parsed.Items, latencyMS, nil
}

// UploadBackup streams a migration archive to POST /api/v1/backups using multipart/form-data.
func (c *Client) UploadBackup(ctx context.Context, baseURL, apiKey, filename string, data []byte, opts UploadOptions) (*UploadResult, error) {
	cleanURL, err := ValidateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if cleanURL == "" {
		return nil, apperror.New(apperror.CodeInvalidArgument, "safekeep base_url is required")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, apperror.New(apperror.CodeInvalidArgument, "safekeep api_key is required")
	}
	if strings.TrimSpace(filename) == "" {
		filename = "engram-migration.zip"
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if len(opts.Metadata) > 0 {
		metaBytes, err := json.Marshal(opts.Metadata)
		if err == nil {
			_ = mw.WriteField("metadata", string(metaBytes))
		}
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "create multipart file field", err)
	}
	if _, err := fw.Write(data); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "write multipart file field", err)
	}
	if err := mw.Close(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "close multipart writer", err)
	}

	q := url.Values{}
	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	if mode != "sync" && mode != "async" {
		mode = "sync"
	}
	q.Set("mode", mode)
	if p := strings.TrimSpace(opts.Path); p != "" {
		q.Set("path", p)
	}
	if t := strings.TrimSpace(opts.Targets); t != "" {
		q.Set("targets", t)
	}

	endpoint := cleanURL + "/api/v1/backups?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument, "build safekeep upload request", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-API-Key", apiKey)

	sum := sha256.Sum256(data)
	req.Header.Set("X-Content-SHA256", hex.EncodeToString(sum[:]))
	if strings.TrimSpace(opts.IdempotencyKey) != "" {
		req.Header.Set("Idempotency-Key", strings.TrimSpace(opts.IdempotencyKey))
	}

	resp, err := c.uploadHTTP.Do(req)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "upload backup to safekeep failed: "+err.Error(), err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseSafekeepHTTPError(resp.StatusCode, respBody)
	}

	var parsed struct {
		Task *struct {
			ID               string `json:"id"`
			Status           string `json:"status"`
			OriginalFilename string `json:"original_filename"`
			FileSize         int64  `json:"file_size"`
			SHA256           string `json:"sha256"`
			Mode             string `json:"mode"`
			Deduplicated     bool   `json:"deduplicated"`
		} `json:"task"`
		TimedOutToAsync  bool `json:"timed_out_to_async"`
		IdempotentReplay bool `json:"idempotent_replay"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "decode safekeep upload response", err)
	}

	out := &UploadResult{
		TimedOutToAsync:  parsed.TimedOutToAsync,
		IdempotentReplay: parsed.IdempotentReplay,
	}
	if parsed.Task != nil {
		out.TaskID = parsed.Task.ID
		out.Status = parsed.Task.Status
		out.OriginalFilename = parsed.Task.OriginalFilename
		out.FileSize = parsed.Task.FileSize
		out.SHA256 = parsed.Task.SHA256
		out.Mode = parsed.Task.Mode
		out.Deduplicated = parsed.Task.Deduplicated
	}
	return out, nil
}

func parseSafekeepHTTPError(status int, body []byte) error {
	var errPayload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := fmt.Sprintf("safekeep returned HTTP %d", status)
	if json.Unmarshal(body, &errPayload) == nil && errPayload.Error.Message != "" {
		msg = fmt.Sprintf("safekeep (%d %s): %s", status, errPayload.Error.Code, errPayload.Error.Message)
	}
	switch status {
	case http.StatusUnauthorized:
		return apperror.New(apperror.CodeUnauthenticated, msg)
	case http.StatusForbidden:
		return apperror.New(apperror.CodeUnauthorized, msg)
	case http.StatusTooManyRequests:
		return apperror.New(apperror.CodeRateLimited, msg)
	case http.StatusBadRequest:
		return apperror.New(apperror.CodeInvalidArgument, msg)
	default:
		return apperror.New(apperror.CodeInternalError, msg)
	}
}
