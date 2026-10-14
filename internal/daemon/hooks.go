package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Hook pushes matching events to a URL as JSON.
//
//	POST <url>
//	X-Gbd-Event: message            X-Gbd-Delivery: <event id>
//	X-Gbd-Timestamp: <unix seconds> X-Gbd-Signature: sha256=hex(HMAC(secret, "<timestamp>.<body>"))
//
// Delivery is at-least-once with 3 retries (1s, 5s, 25s); receivers should dedupe by X-Gbd-Delivery.
type Hook struct {
	URL    string   `json:"url"`
	Secret string   `json:"secret,omitempty"`
	Events []string `json:"events,omitempty"` // empty = all
	Bots   []string `json:"bots,omitempty"`   // names or ids; empty = all
}

func Sign(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d.", ts)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// RunHooks delivers events to each hook from its own goroutine so a slow endpoint never blocks others.
func RunHooks(ctx context.Context, h *Hub, hooks []Hook, lg *log.Logger) {
	for i := range hooks {
		hk := hooks[i]
		go func() {
			id, ch, _ := h.Subscribe(0)
			defer h.Unsubscribe(id)
			events := map[string]bool{}
			for _, e := range hk.Events {
				events[e] = true
			}
			var bots map[string]bool // resolved once (names → ids) on first use
			resolveBots := func() {
				bots = map[string]bool{}
				for _, r := range hk.Bots {
					if a, err := h.API.Resolve(ctx, r); err == nil {
						bots[a.ID] = true
					} else {
						lg.Printf("hook %s: bot %q: %v", hk.URL, r, err)
					}
				}
			}
			client := &http.Client{Timeout: 15 * time.Second}
			for {
				select {
				case <-ctx.Done():
					return
				case e, ok := <-ch:
					if !ok {
						lg.Printf("hook %s fell behind; resubscribing", hk.URL)
						id, ch, _ = h.Subscribe(0)
						continue
					}
					if len(events) > 0 && !events[e.Type] {
						continue
					}
					if len(hk.Bots) > 0 {
						if bots == nil {
							resolveBots()
						}
						if !bots[e.AgentID] {
							// A bot created after startup: accept it if its name is listed.
							for _, r := range hk.Bots {
								if e.AgentName != "" && strings.EqualFold(r, e.AgentName) {
									bots[e.AgentID] = true
								}
							}
							if !bots[e.AgentID] {
								continue
							}
						}
					}
					deliver(ctx, client, hk, e, lg)
				}
			}
		}()
	}
}

func deliver(ctx context.Context, client *http.Client, hk Hook, e Event, lg *log.Logger) {
	body, _ := json.Marshal(e)
	for attempt, wait := 0, time.Second; attempt < 4; attempt, wait = attempt+1, wait*5 {
		ts := time.Now().Unix()
		req, _ := http.NewRequestWithContext(ctx, "POST", hk.URL, bytes.NewReader(body))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("user-agent", "gbd")
		req.Header.Set("x-gbd-event", e.Type)
		req.Header.Set("x-gbd-delivery", strconv.FormatInt(e.ID, 10))
		req.Header.Set("x-gbd-timestamp", strconv.FormatInt(ts, 10))
		if hk.Secret != "" {
			req.Header.Set("x-gbd-signature", Sign(hk.Secret, ts, body))
		}
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode < 300 {
				return
			}
			if res.StatusCode >= 400 && res.StatusCode < 500 && res.StatusCode != 429 {
				lg.Printf("hook %s rejected event %d: HTTP %d", hk.URL, e.ID, res.StatusCode)
				return
			}
			err = fmt.Errorf("HTTP %d", res.StatusCode)
		}
		if attempt == 3 {
			lg.Printf("hook %s: giving up on event %d: %v", hk.URL, e.ID, err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
