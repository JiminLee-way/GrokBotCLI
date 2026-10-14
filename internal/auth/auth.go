// Package auth implements the Grok Bot (Cursor) login, refresh and logout flows without the desktop app.
//
//	login:   https://cursor.com/loginDeepControl?challenge&uuid&mode=login&redirectTarget=sand  (PKCE)
//	poll:    GET  {backend}/auth/poll?uuid&verifier        -> {accessToken, refreshToken}
//	api key: POST {backend}/auth/exchange_user_api_key     -> {accessToken, refreshToken}
//	refresh: POST {backend}/oauth/token {client_id, grant_type:"refresh_token", refresh_token}
//	logout:  POST {backend}/auth/logout (Bearer access token)
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"gb/internal/config"
)

var HTTP = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("refusing to follow redirect with credentials")
	},
}

var ErrPolicy = errors.New("sign-in blocked by organization device policy (sign_in_policy_violation)")

// ErrSignedOut means the refresh token was refused and the user must log in again.
var ErrSignedOut = errors.New("session expired or revoked; run `gb login`")

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func UUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Checksum reproduces the desktop x-cursor-checksum header (main-app.cjs createCursorChecksum).
func Checksum(machineID string) string { return checksumAt(time.Now().UnixMilli(), machineID) }

func checksumAt(nowMs int64, machineID string) string {
	t := uint32(nowMs / 1e6)
	// JS `>>` takes the shift count mod 32, so the app's `t>>40` and `t>>32` are really `t>>8` and `t>>0`.
	var lo [4]byte
	binary.BigEndian.PutUint32(lo[:], t)
	e := []byte{lo[2], lo[3], lo[0], lo[1], lo[2], lo[3]}
	prev := byte(165)
	for n := range e {
		e[n] = (e[n] ^ prev) + byte(n%256)
		prev = e[n]
	}
	return b64url(e) + machineID
}

// BackendHeaders are the headers the desktop app sends to api2.cursor.sh.
func BackendHeaders(c *config.Credentials) http.Header {
	h := http.Header{}
	h.Set("content-type", "application/json")
	h.Set("connect-protocol-version", "1")
	h.Set("user-agent", "gb/"+config.ClientVersion()+" (Sand/"+config.ClientVersion()+"; "+runtime.GOOS+")")
	h.Set("x-cursor-client-type", "sand")
	h.Set("x-cursor-client-version", config.ClientVersion())
	h.Set("x-sand-box-namespace", config.Namespace())
	h.Set("x-cursor-client-os", runtime.GOOS)
	h.Set("x-cursor-client-source", "sand-desktop")
	h.Set("x-ghost-mode", "true")
	h.Set("x-request-id", UUID())
	if c != nil {
		if c.AccessToken != "" {
			h.Set("authorization", "Bearer "+c.AccessToken)
		}
		if c.MachineID != "" {
			h.Set("x-cursor-checksum", Checksum(c.MachineID))
		}
		if c.TeamID != "" {
			h.Set("x-cursor-team-id", c.TeamID)
		}
	}
	return h
}

type LoginRequest struct {
	UUID     string `json:"uuid"`
	Verifier string `json:"verifier"`
	URL      string `json:"loginUrl"`
}

func StartLogin() *LoginRequest {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	verifier := b64url(raw)
	sum := sha256.Sum256([]byte(verifier))
	id := UUID()
	u, _ := url.Parse(config.Website() + "/loginDeepControl")
	q := u.Query()
	q.Set("challenge", b64url(sum[:]))
	q.Set("uuid", id)
	q.Set("mode", "login")
	q.Set("redirectTarget", "sand")
	q.Set("supportsSelectedTeamLogin", "true")
	u.RawQuery = q.Encode()
	return &LoginRequest{UUID: id, Verifier: verifier, URL: u.String()}
}

type Tokens struct {
	AccessToken    string `json:"accessToken"`
	RefreshToken   string `json:"refreshToken"`
	SelectedTeamID any    `json:"selectedTeamId,omitempty"`
}

func isPolicy(status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	var e struct{ Error string }
	_ = json.Unmarshal(body, &e)
	return e.Error == "sign_in_policy_violation"
}

// PollLogin waits for the browser approval (404 = not yet), mirroring the app's backoff:
// min(1000*1.2^n, 10000) ms, up to 150 attempts, stop after 3 consecutive non-404 errors.
func PollLogin(ctx context.Context, lr *LoginRequest) (*Tokens, error) {
	u := config.Backend() + "/auth/poll?uuid=" + url.QueryEscape(lr.UUID) + "&verifier=" + url.QueryEscape(lr.Verifier)
	errs := 0
	for n := 0; n < 150; n++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		req.Header.Set("content-type", "application/json")
		res, err := HTTP.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			switch {
			case isPolicy(res.StatusCode, body):
				return nil, ErrPolicy
			case res.StatusCode == 404:
				errs = 0
			case res.StatusCode >= 200 && res.StatusCode < 300:
				t := &Tokens{}
				if json.Unmarshal(body, t) == nil && t.AccessToken != "" && t.RefreshToken != "" {
					return t, nil
				}
				return nil, errors.New("login poll returned an unexpected payload")
			default:
				errs++
			}
		} else {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			errs++
		}
		if errs >= 3 {
			return nil, errors.New("login polling failed 3 times in a row")
		}
		d := time.Duration(math.Min(1000*math.Pow(1.2, float64(n)), 10000)) * time.Millisecond
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d):
		}
	}
	return nil, errors.New("login timed out; run `gb login` again")
}

// ExchangeAPIKey trades a Cursor user API key for an access/refresh pair.
func ExchangeAPIKey(ctx context.Context, key string) (*Tokens, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", config.Backend()+"/auth/exchange_user_api_key", strings.NewReader("{}"))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+key)
	res, err := HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if isPolicy(res.StatusCode, body) {
		return nil, ErrPolicy
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("API key exchange failed: HTTP %d", res.StatusCode)
	}
	t := &Tokens{}
	if json.Unmarshal(body, t) != nil || t.AccessToken == "" {
		return nil, errors.New("API key exchange returned no tokens")
	}
	return t, nil
}

// Refresh exchanges the refresh token. Rotation is optional: keep the old refresh token if none is returned.
func Refresh(ctx context.Context, refresh string) (access, newRefresh string, err error) {
	body, _ := json.Marshal(map[string]string{
		"client_id": config.ClientID(), "grant_type": "refresh_token", "refresh_token": refresh,
	})
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", config.Backend()+"/oauth/token", strings.NewReader(string(body)))
	req.Header.Set("content-type", "application/json")
	res, err := HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if isPolicy(res.StatusCode, raw) {
		return "", "", ErrPolicy
	}
	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ShouldLogout bool   `json:"shouldLogout"`
	}
	if res.StatusCode != 200 {
		return "", "", fmt.Errorf("token refresh failed: HTTP %d", res.StatusCode)
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", "", errors.New("token refresh returned a bad payload")
	}
	if r.ShouldLogout {
		return "", "", ErrSignedOut
	}
	if r.AccessToken == "" {
		return "", "", errors.New("token refresh returned no access token")
	}
	if r.RefreshToken == "" {
		r.RefreshToken = refresh
	}
	return r.AccessToken, r.RefreshToken, nil
}

// Logout revokes the server session (best effort).
func Logout(ctx context.Context, access string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", config.Backend()+"/auth/logout", nil)
	req.Header.Set("authorization", "Bearer "+access)
	res, err := HTTP.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("server logout returned HTTP %d", res.StatusCode)
	}
	return nil
}

// Claims decodes (without verifying) the JWT payload.
func Claims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	m := map[string]any{}
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func Expiry(token string) time.Time {
	if f, ok := Claims(token)["exp"].(float64); ok {
		return time.Unix(int64(f), 0)
	}
	return time.Time{}
}

// Session returns credentials with a fresh access token (refreshing when <5 min remain).
func Session(ctx context.Context, p *config.Profile) (*config.Credentials, error) {
	c, err := p.Credentials()
	if err != nil {
		return nil, err
	}
	if c.AccessToken == "" && c.RefreshToken == "" {
		return nil, errors.New("not logged in; run `gb login`")
	}
	exp := Expiry(c.AccessToken)
	if c.AccessToken != "" && (exp.IsZero() || time.Until(exp) > 5*time.Minute) {
		return c, nil
	}
	if c.RefreshToken == "" {
		if c.AccessToken != "" && time.Until(exp) > 0 {
			return c, nil
		}
		return nil, ErrSignedOut
	}
	return ForceRefresh(ctx, p, c)
}

var refreshMu sync.Mutex

// ForceRefresh rotates the token pair. It is single-flight within the process and serialized across
// processes (CLI + daemon) with a profile file lock; if another holder already refreshed, its result is reused.
func ForceRefresh(ctx context.Context, p *config.Profile, c *config.Credentials) (*config.Credentials, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	unlock, err := p.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if cur, err := p.Credentials(); err == nil && cur.AccessToken != "" && cur.AccessToken != c.AccessToken &&
		time.Until(Expiry(cur.AccessToken)) > 5*time.Minute {
		return cur, nil
	}
	if cur, err := p.Credentials(); err == nil && cur.RefreshToken != "" {
		c = cur
	}
	if c.RefreshToken == "" {
		return nil, ErrSignedOut
	}
	a, r, err := Refresh(ctx, c.RefreshToken)
	if err != nil {
		return nil, err
	}
	c.AccessToken, c.RefreshToken = a, r
	return c, p.SaveCredentials(c)
}

func StoreTokens(p *config.Profile, t *Tokens) error {
	c, err := p.Credentials()
	if err != nil {
		return err
	}
	c.AccessToken, c.RefreshToken = t.AccessToken, t.RefreshToken
	switch v := t.SelectedTeamID.(type) {
	case string:
		c.TeamID = v
	case float64:
		c.TeamID = fmt.Sprintf("%.0f", v)
	}
	_ = p.ClearBox()
	_ = p.Remove(config.AgentsCache)
	return p.SaveCredentials(c)
}
