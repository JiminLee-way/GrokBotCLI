// Package box connects to the user's cloud "box" gateway.
//
//	EnsureSandBox: POST {backend}/aiserver.v1.GrokBotService/EnsureSandBox (Connect JSON)
//	methods:       POST {gatewayUrl}/api/<method>  Bearer gatewayToken + x-anyrun-network-token
//	events:        GET  {gatewayUrl}/events        text/event-stream, `data: {channel, payload}`
package box

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gb/internal/auth"
	"gb/internal/config"
)

// GatewayError is a failed /api call. Status 0 means the request never got an HTTP answer.
type GatewayError struct {
	Method      string `json:"method"`
	Status      int    `json:"status"`
	Message     string `json:"message"`
	FailureCode string `json:"failureCode,omitempty"`
	// Delivery is "rejected" when the server definitely refused, "unknown" when it may have landed.
	Delivery string `json:"delivery"`
}

func (e *GatewayError) Error() string {
	s := e.Method + " failed"
	if e.Status > 0 {
		s += fmt.Sprintf(": HTTP %d", e.Status)
	}
	if e.FailureCode != "" {
		s += " [" + e.FailureCode + "]"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

var allowedGatewaySuffixes = []string{".cursor.sh", ".cursor.com", ".cursorvm.com"}

func checkGatewayURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid gateway url %q", raw)
	}
	if os.Getenv("GB_ALLOW_ANY_GATEWAY") == "1" {
		return u.String(), nil
	}
	host := u.Hostname()
	if os.Getenv("GB_ALLOW_LOCAL_GATEWAY") == "1" && (host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return u.String(), nil
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("refusing non-https gateway %s", host)
	}
	for _, s := range allowedGatewaySuffixes {
		if strings.HasSuffix(host, s) {
			return u.String(), nil
		}
	}
	return "", fmt.Errorf("refusing to send credentials to unexpected gateway host %s (set GB_ALLOW_ANY_GATEWAY=1 to override)", host)
}

// EnsureResult is the subset of EnsureSandBoxResponse we use (plus the raw body for `gb box status --json`).
type EnsureResult struct {
	GatewayURL           string `json:"gatewayUrl"`
	GatewayToken         string `json:"gatewayToken"`
	NetworkToken         string `json:"networkToken"`
	RunState             string `json:"runState"`
	Cluster              string `json:"cluster"`
	PodID                string `json:"podId"`
	ImageUpdateAvailable bool   `json:"imageUpdateAvailable"`
}

func backendCall(ctx context.Context, c *config.Credentials, rpc string, body any, out any) (int, error) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", config.Backend()+"/"+rpc, bytes.NewReader(b))
	req.Header = auth.BackendHeaders(c)
	res, err := auth.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != 200 {
		var ce struct{ Code, Message string }
		_ = json.Unmarshal(raw, &ce)
		hint := res.Header.Get("x-automation-failure-hint")
		msg := strings.TrimSpace(ce.Code + " " + ce.Message + " " + hint)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return res.StatusCode, fmt.Errorf("%s: HTTP %d %s", rpc, res.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.StatusCode, fmt.Errorf("%s: bad response: %w", rpc, err)
		}
	}
	return 200, nil
}

// Backend calls an aiserver Connect RPC with a fresh access token, refreshing once on 401.
func Backend(ctx context.Context, p *config.Profile, rpc string, body any, out any) error {
	c, err := auth.Session(ctx, p)
	if err != nil {
		return err
	}
	status, err := backendCall(ctx, c, rpc, body, out)
	if status == 401 {
		if c, err = auth.ForceRefresh(ctx, p, c); err != nil {
			return err
		}
		_, err = backendCall(ctx, c, rpc, body, out)
	}
	return err
}

// BackendTeam is Backend with an explicit x-cursor-team-id (team-scoped RPCs such as GetTeamMembers).
func BackendTeam(ctx context.Context, p *config.Profile, teamID, rpc string, body any, out any) error {
	c, err := auth.Session(ctx, p)
	if err != nil {
		return err
	}
	tc := *c
	tc.TeamID = teamID
	status, err := backendCall(ctx, &tc, rpc, body, out)
	if status == 401 {
		if c, err = auth.ForceRefresh(ctx, p, c); err != nil {
			return err
		}
		tc = *c
		tc.TeamID = teamID
		_, err = backendCall(ctx, &tc, rpc, body, out)
	}
	return err
}

// Ensure provisions/wakes the box and stores the gateway connection.
func Ensure(ctx context.Context, p *config.Profile, wake bool) (*config.BoxConn, *EnsureResult, error) {
	r := &EnsureResult{}
	if err := Backend(ctx, p, "aiserver.v1.GrokBotService/EnsureSandBox", map[string]any{"wake": wake, "supportsAsleepBox": true}, r); err != nil {
		return nil, nil, err
	}
	if r.GatewayURL == "" || r.GatewayToken == "" {
		return nil, r, fmt.Errorf("box not ready (runState=%s); try `gb box wake`", r.RunState)
	}
	gw, err := checkGatewayURL(r.GatewayURL)
	if err != nil {
		return nil, r, err
	}
	conn := &config.BoxConn{GatewayURL: gw, GatewayToken: r.GatewayToken, NetworkToken: r.NetworkToken, RunState: r.RunState, SavedAtMs: time.Now().UnixMilli()}
	return conn, r, p.SaveBox(conn)
}

// Client talks to the box gateway, re-provisioning the connection when it goes stale.
type Client struct {
	P    *config.Profile
	HTTP *http.Client

	mu   sync.Mutex
	conn *config.BoxConn
}

func New(p *config.Profile) *Client {
	return &Client{P: p, HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: auth.HTTP.CheckRedirect}}
}

// connection returns the gateway connection. stale != nil means that connection just failed and must be
// replaced; concurrent callers that saw the same stale connection share one EnsureSandBox (single-flight).
func (c *Client) connection(ctx context.Context, stale *config.BoxConn, wake bool) (*config.BoxConn, error) {
	if env := os.Getenv("GB_GATEWAY_URL"); env != "" && os.Getenv("GB_GATEWAY_TOKEN") != "" {
		gw, err := checkGatewayURL(env)
		return &config.BoxConn{GatewayURL: gw, GatewayToken: os.Getenv("GB_GATEWAY_TOKEN"), NetworkToken: os.Getenv("GB_GATEWAY_NETWORK_TOKEN")}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil && c.conn != stale {
		return c.conn, nil
	}
	if stale == nil {
		if b, err := c.P.Box(); err == nil && b.GatewayURL != "" {
			c.conn = b
			return b, nil
		}
	}
	b, _, err := Ensure(ctx, c.P, wake)
	if err != nil {
		return nil, err
	}
	c.conn = b
	return b, nil
}

func (c *Client) headers(b *config.BoxConn, h http.Header) {
	h.Set("authorization", "Bearer "+b.GatewayToken)
	if b.NetworkToken != "" {
		h.Set("x-anyrun-network-token", b.NetworkToken)
	}
	h.Set("x-sand-slim-avatars", "1")
}

func asleep(res *http.Response) bool {
	return res.StatusCode == 417 || strings.HasPrefix(res.Header.Get("x-anyrun-failure-reason"), "pod_hibernated")
}

// Call invokes POST /api/<method>. args nil sends {}. out may be nil.
// Retries once with a fresh EnsureSandBox on 401/403 and wakes the box on 417; these are refusals, so retrying is safe.
// Transport errors on sendPrompt are reported with Delivery "unknown" and never retried blindly.
func (c *Client) Call(ctx context.Context, method string, args any, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var stale *config.BoxConn
	wake := false
	for attempt := 0; ; attempt++ {
		b, err := c.connection(ctx, stale, wake)
		if err != nil {
			return err
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", b.GatewayURL+"/api/"+method, bytes.NewReader(body))
		req.Header.Set("content-type", "application/json")
		c.headers(b, req.Header)
		res, err := c.HTTP.Do(req)
		if err != nil {
			// A stale cached gateway (DNS gone, box moved) fails before sending: re-ensure once.
			if attempt == 0 && method != "sendPrompt" {
				stale = b
				continue
			}
			return &GatewayError{Method: method, Message: err.Error(), Delivery: "unknown"}
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		res.Body.Close()
		if attempt < 2 && (res.StatusCode == 401 || res.StatusCode == 403 || asleep(res)) {
			stale, wake = b, asleep(res)
			if wake {
				if err := c.waitAwake(ctx); err != nil {
					return err
				}
				stale = nil
			}
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			ge := &GatewayError{Method: method, Status: res.StatusCode, Delivery: "rejected"}
			var e struct {
				Error       string `json:"error"`
				FailureCode string `json:"failureCode"`
			}
			if json.Unmarshal(raw, &e) == nil && e.Error != "" {
				ge.Message, ge.FailureCode = e.Error, e.FailureCode
			} else {
				ge.Message = strings.TrimSpace(string(raw))
				if len(ge.Message) > 300 {
					ge.Message = ge.Message[:300]
				}
			}
			if res.StatusCode >= 500 {
				ge.Delivery = "unknown"
			}
			return ge
		}
		if out == nil || len(bytes.TrimSpace(raw)) == 0 {
			return nil
		}
		return json.Unmarshal(raw, out)
	}
}

// waitAwake asks EnsureSandBox to wake the box and polls until the gateway answers /health.
func (c *Client) waitAwake(ctx context.Context) error {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		b, r, err := Ensure(ctx, c.P, true)
		if err == nil {
			c.mu.Lock()
			c.conn = b
			c.mu.Unlock()
			if c.Health(ctx) == nil {
				return nil
			}
		} else if r == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return errors.New("box did not wake up within 3 minutes")
}

// Health checks GET /health (network token only, as the app does).
func (c *Client) Health(ctx context.Context) error {
	b, err := c.connection(ctx, nil, false)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", b.GatewayURL+"/health", nil)
	if b.NetworkToken != "" {
		req.Header.Set("x-anyrun-network-token", b.NetworkToken)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("gateway /health: HTTP %d", res.StatusCode)
	}
	return nil
}

// Event is one SSE frame from /events.
type Event struct {
	Channel string          `json:"channel"`
	Payload json.RawMessage `json:"payload"`
}

// Events streams /events forever (until ctx is done), reconnecting with 1-2-4-8-10s backoff.
// onConnect runs after every (re)connect so callers can refetch state they may have missed.
func (c *Client) Events(ctx context.Context, onConnect func(), on func(Event)) error {
	backoff := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second}
	fails := 0
	var stale *config.BoxConn
	for ctx.Err() == nil {
		err := c.streamOnce(ctx, stale, onConnect, on)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var se *streamErr
		if errors.As(err, &se) && se.connected {
			fails = 0
		}
		stale = nil
		if errors.As(err, &se) && se.reauth {
			stale = se.conn
		}
		d := backoff[min(fails, len(backoff)-1)]
		fails++
		if os.Getenv("GB_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "events: %v; reconnecting in %s\n", err, d)
		}
		select {
		case <-ctx.Done():
		case <-time.After(d):
		}
	}
	return ctx.Err()
}

type streamErr struct {
	conn      *config.BoxConn
	err       error
	connected bool
	reauth    bool
}

func (e *streamErr) Error() string { return e.err.Error() }

func (c *Client) streamOnce(ctx context.Context, stale *config.BoxConn, onConnect func(), on func(Event)) error {
	b, err := c.connection(ctx, stale, false)
	if err != nil {
		return &streamErr{err: err}
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(sctx, "GET", b.GatewayURL+"/events", nil)
	req.Header.Set("accept", "text/event-stream")
	c.headers(b, req.Header)
	hc := &http.Client{CheckRedirect: c.HTTP.CheckRedirect} // no overall timeout for streams
	res, err := hc.Do(req)
	if err != nil {
		return &streamErr{conn: b, err: err, reauth: true}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		if asleep(res) {
			_ = c.waitAwake(ctx)
		}
		return &streamErr{conn: b, err: fmt.Errorf("events: HTTP %d", res.StatusCode), reauth: res.StatusCode == 401 || res.StatusCode == 403}
	}
	if onConnect != nil {
		onConnect()
	}
	// Stall watchdog: the gateway emits frames/comments regularly; 45s of silence means a dead stream.
	alive := make(chan struct{}, 1)
	go func() {
		t := time.NewTimer(45 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-alive:
				t.Reset(45 * time.Second)
			case <-t.C:
				cancel()
				return
			}
		}
	}()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	var data strings.Builder
	for sc.Scan() {
		select {
		case alive <- struct{}{}:
		default:
		}
		line := sc.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				var ev Event
				if json.Unmarshal([]byte(data.String()), &ev) == nil && ev.Channel != "" {
					on(ev)
				}
				data.Reset()
			}
		case strings.HasPrefix(line, ":"):
			// comment / ":echo <nonce>" liveness frame
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	err = sc.Err()
	if err == nil {
		err = io.EOF
	}
	return &streamErr{err: fmt.Errorf("events stream ended: %w", err), connected: true}
}
