package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// ErrDaemonUnreachable is returned by Client methods when the admin socket
// is missing or refuses connection — typically because slackrun start is
// not running. Callers surface it as a distinct exit code so scripts can
// distinguish "no daemon" from "kill failed".
var ErrDaemonUnreachable = errors.New("slackrun daemon not reachable")

// Client is a thin JSON client over the admin UDS. Not goroutine-safe; the
// CLI uses one per invocation and throws it away.
type Client struct {
	http    *http.Client
	baseURL string // e.g. http://slackrun/v1
}

// NewClient dials the socket at path (usually ResolveSocketPath()'s output)
// lazily on each request. Returns immediately; connection errors surface
// on the first call.
func NewClient(path string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
	return &Client{
		http:    &http.Client{Transport: transport, Timeout: 10 * time.Second},
		baseURL: "http://slackrun/" + APIVersion,
	}
}

// NewClientFromEnv resolves the socket the same way the server does and
// returns a ready client. When the operator has disabled the admin API
// (SLACKRUN_ADMIN_SOCKET=off), ResolveSocketPath returns ErrDisabled; that
// bubbles up here so the CLI can print a clear message.
func NewClientFromEnv() (*Client, string, error) {
	path, err := ResolveSocketPath()
	if err != nil {
		return nil, "", err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, path, ErrDaemonUnreachable
	}
	return NewClient(path), path, nil
}

// Runs returns the current list of in-flight runs.
func (c *Client) Runs(ctx context.Context) ([]RunView, error) {
	var out []RunView
	if err := c.get(ctx, "/runs", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Kill sends SIGTERM to one run. Reason (when non-empty) is echoed to the
// Slack thread.
func (c *Client) Kill(ctx context.Context, id, reason string) (KillResponse, error) {
	body := map[string]any{}
	if reason != "" {
		body["reason"] = reason
	}
	var out KillResponse
	if err := c.post(ctx, "/runs/"+id+"/kill", body, &out); err != nil {
		return out, err
	}
	return out, nil
}

// KillAll signals every currently-running child.
func (c *Client) KillAll(ctx context.Context, reason string) (KillAllResponse, error) {
	body := map[string]any{}
	if reason != "" {
		body["reason"] = reason
	}
	var out KillAllResponse
	if err := c.post(ctx, "/runs/kill-all", body, &out); err != nil {
		return out, err
	}
	return out, nil
}

// Health returns liveness info from the daemon.
func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	var out HealthResponse
	if err := c.get(ctx, "/health", &out); err != nil {
		return out, err
	}
	return out, nil
}

// RunView mirrors the server's runView shape for JSON round-trip. Kept in
// the client package so callers don't import the server type.
type RunView struct {
	ID         string `json:"id"`
	FullID     string `json:"full_id"`
	Rule       string `json:"rule"`
	ChannelID  string `json:"channel_id"`
	UserID     string `json:"user_id,omitempty"`
	ThreadTS   string `json:"thread_ts,omitempty"`
	StartedAt  string `json:"started_at"`
	ElapsedMs  int64  `json:"elapsed_ms"`
	State      string `json:"state"`
	PID        int    `json:"pid,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	ExitCause  string `json:"exit_cause,omitempty"`
	KillReason string `json:"kill_reason,omitempty"`
}

// KillResponse is what /v1/runs/{id}/kill returns on success.
type KillResponse struct {
	Killed bool   `json:"killed"`
	ID     string `json:"id"`
	FullID string `json:"full_id"`
}

// KillAllResponse is what /v1/runs/kill-all returns.
type KillAllResponse struct {
	Killed    bool     `json:"killed"`
	KilledIDs []string `json:"killed_ids"`
}

// HealthResponse mirrors the /v1/health payload.
type HealthResponse struct {
	OK         bool   `json:"ok"`
	Version    string `json:"version"`
	UptimeMs   int64  `json:"uptime_ms"`
	ActiveRuns int    `json:"active_runs"`
}

// APIError is returned when the server responds with a non-2xx status.
// Callers switch on Code to differentiate not_found / not_killable / etc.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("admin api: %d %s", e.StatusCode, e.Code)
	}
	return fmt.Sprintf("admin api: %d %s: %s", e.StatusCode, e.Code, e.Message)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		// A missing socket surfaces here as a net.OpError with ENOENT/refused.
		return ErrDaemonUnreachable
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var eb errBody
		body, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(body, &eb)
		return &APIError{StatusCode: resp.StatusCode, Code: eb.Error.Code, Message: eb.Error.Message}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
