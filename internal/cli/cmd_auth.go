package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gb/internal/auth"
	"gb/internal/box"
	"gb/internal/config"
)

func cmdLogin(ctx context.Context, e *env) error {
	p, err := config.Open(e.profile)
	if err != nil {
		return err
	}
	var t *auth.Tokens
	if key, ok := e.a.str("api-key"); ok {
		if key == "" || key == "-" {
			b, _ := io.ReadAll(io.LimitReader(os.Stdin, 4096))
			key = string(b)
		}
		if t, err = auth.ExchangeAPIKey(ctx, trimSpace(key)); err != nil {
			return err
		}
	} else {
		lr := auth.StartLogin()
		if e.json {
			e.emit(map[string]any{"status": "pending", "loginUrl": lr.URL}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "Open this URL in any browser and approve the login:\n\n  %s\n\nWaiting for approval (Ctrl-C to cancel)...\n", lr.URL)
		}
		if t, err = auth.PollLogin(ctx, lr); err != nil {
			return err
		}
	}
	if err := auth.StoreTokens(p, t); err != nil {
		return err
	}
	info := whoInfo(p)
	// Provision the box now so the first real command is fast; failure here is not fatal.
	if _, r, berr := box.Ensure(ctx, p, false); berr == nil && r != nil {
		info["box"] = r.RunState
	} else if berr != nil {
		info["boxError"] = berr.Error()
	}
	e.emit(info, func(w io.Writer) {
		fmt.Fprintf(w, "Logged in as %v (profile %s).", info["email"], p.Name)
		if b, ok := info["box"]; ok {
			fmt.Fprintf(w, " Box: %v.", b)
		}
		fmt.Fprintln(w)
		if be, ok := info["boxError"]; ok {
			fmt.Fprintf(w, "warning: box not ready yet: %v\n", be)
		}
	})
	return nil
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	return s
}

func whoInfo(p *config.Profile) map[string]any {
	c, _ := p.Credentials()
	info := map[string]any{"profile": p.Name, "loggedIn": c != nil && (c.AccessToken != "" || c.RefreshToken != "")}
	if c == nil {
		return info
	}
	claims := auth.Claims(c.AccessToken)
	for _, k := range []string{"email", "sub"} {
		if v, ok := claims[k]; ok {
			info[k] = v
		}
	}
	if _, ok := info["email"]; !ok {
		info["email"] = info["sub"]
	}
	if exp := auth.Expiry(c.AccessToken); !exp.IsZero() {
		info["accessTokenExpires"] = exp.Format(time.RFC3339)
	}
	info["hasRefreshToken"] = c.RefreshToken != ""
	if c.TeamID != "" {
		info["teamId"] = c.TeamID
	}
	return info
}

func cmdLogout(ctx context.Context, e *env) error {
	p, err := config.Open(e.profile)
	if err != nil {
		return err
	}
	c, _ := p.Credentials()
	var serverErr error
	if c != nil && c.AccessToken != "" {
		if other := sharedWith(p.Name, c.AccessToken); other != "" {
			serverErr = fmt.Errorf("kept the server session: account %q is signed in as the same user", other)
		} else {
			serverErr = auth.Logout(ctx, c.AccessToken)
		}
	}
	if err := p.ClearAuth(); err != nil {
		return err
	}
	res := map[string]any{"loggedOut": true, "serverRevoked": serverErr == nil}
	if serverErr != nil {
		res["serverError"] = serverErr.Error()
	}
	e.emit(res, func(w io.Writer) {
		fmt.Fprintln(w, "Logged out; local credentials removed.")
		if serverErr != nil {
			fmt.Fprintln(w, "note: server-side revoke failed:", serverErr)
		}
	})
	return nil
}

func cmdWhoami(ctx context.Context, e *env) error {
	p, err := config.Open(e.profile)
	if err != nil {
		return err
	}
	if _, err := auth.Session(ctx, p); err != nil && !errors.Is(err, auth.ErrSignedOut) {
		return err
	}
	info := whoInfo(p)
	e.emit(info, func(w io.Writer) {
		if info["loggedIn"] != true {
			fmt.Fprintln(w, "Not logged in. Run `gb login`.")
			return
		}
		fmt.Fprintf(w, "%v  (profile %s, token valid until %v)\n", info["email"], p.Name, info["accessTokenExpires"])
	})
	return nil
}

func cmdBox(ctx context.Context, e *env) error {
	sub := "status"
	if len(e.a.pos) > 0 {
		sub = e.a.pos[0]
	}
	p, err := config.Open(e.profile)
	if err != nil {
		return err
	}
	switch sub {
	case "status", "wake":
		conn, r, err := box.Ensure(ctx, p, sub == "wake")
		if err != nil && r == nil {
			return err
		}
		out := map[string]any{"runState": r.RunState, "cluster": r.Cluster, "podId": r.PodID, "imageUpdateAvailable": r.ImageUpdateAvailable}
		if conn != nil {
			out["gatewayUrl"] = conn.GatewayURL
			c := box.New(p)
			if herr := c.Health(ctx); herr != nil {
				out["health"] = herr.Error()
			} else {
				out["health"] = "ok"
			}
		}
		e.emit(out, func(w io.Writer) {
			fmt.Fprintf(w, "box: %v  cluster=%v  health=%v\n", r.RunState, r.Cluster, out["health"])
			if conn != nil {
				fmt.Fprintf(w, "gateway: %s\n", conn.GatewayURL)
			}
		})
		return err
	}
	return usage("gb box status|wake")
}

func cmdDoctor(ctx context.Context, e *env) error {
	p, err := config.Open(e.profile)
	if err != nil {
		return err
	}
	type check struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	var checks []check
	add := func(n string, ok bool, d string) { checks = append(checks, check{n, ok, d}) }

	c, err := auth.Session(ctx, p)
	if err != nil {
		add("credentials", false, err.Error())
	} else {
		add("credentials", true, "access token valid until "+auth.Expiry(c.AccessToken).Format(time.RFC3339))
		conn, r, berr := box.Ensure(ctx, p, false)
		switch {
		case berr != nil && r == nil:
			add("EnsureSandBox", false, berr.Error())
		case conn == nil:
			add("EnsureSandBox", false, "runState="+r.RunState+" (gb box wake)")
		default:
			add("EnsureSandBox", true, "runState="+r.RunState+" gateway="+conn.GatewayURL)
			cl := box.New(p)
			if herr := cl.Health(ctx); herr != nil {
				add("gateway /health", false, herr.Error())
			} else {
				add("gateway /health", true, "ok")
			}
			var n float64
			if cerr := cl.Call(ctx, "countAgents", nil, &n); cerr != nil {
				add("gateway /api", false, cerr.Error())
			} else {
				add("gateway /api", true, fmt.Sprintf("countAgents=%.0f", n))
			}
			sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			got := make(chan string, 1)
			go func() {
				_ = cl.Events(sctx, func() {
					select {
					case got <- "connected":
					default:
					}
				}, func(box.Event) {})
			}()
			select {
			case s := <-got:
				add("gateway /events", true, s)
			case <-sctx.Done():
				add("gateway /events", false, "no stream within 8s")
			}
			cancel()
		}
	}
	allOK := true
	for _, ch := range checks {
		allOK = allOK && ch.OK
	}
	e.emit(map[string]any{"ok": allOK, "checks": checks}, func(w io.Writer) {
		for _, ch := range checks {
			mark := "✔"
			if !ch.OK {
				mark = "✘"
			}
			fmt.Fprintf(w, "%s %-16s %s\n", mark, ch.Name, ch.Detail)
		}
	})
	if !allOK {
		return exitError{1, errors.New("doctor found problems")}
	}
	return nil
}
