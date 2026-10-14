package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gb/internal/api"
	"gb/internal/auth"
	"gb/internal/box"
	"gb/internal/config"
)

// apiFor returns the (cached) client for an account profile; "" means the default account.
func (e *env) apiFor(name string) (*api.API, error) {
	if name == "" {
		name = e.profile
	}
	if name == "" {
		name = config.DefaultName()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.apis == nil {
		e.apis = map[string]*api.API{}
	}
	if x := e.apis[name]; x != nil {
		return x, nil
	}
	x, err := api.Open(name)
	if err != nil {
		return nil, err
	}
	e.apis[name] = x
	return x, nil
}

// API returns the client for the selected (or default) account.
func (e *env) API() (*api.API, error) { return e.apiFor("") }

// splitRef splits "account:bot" when the prefix names an existing account.
func splitRef(ref string) (account, rest string) {
	if i := strings.Index(ref, ":"); i > 0 && config.Exists(ref[:i]) {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}

// target resolves a bot reference across accounts:
//   - "work:Researcher"  → that account
//   - "Researcher"       → the selected/default account; if it is not there (and no --profile was
//     given), any other account that has exactly one match
func (e *env) target(ctx context.Context, ref string) (*api.API, *api.Agent, error) {
	if acct, rest := splitRef(ref); acct != "" {
		x, err := e.apiFor(acct)
		if err != nil {
			return nil, nil, err
		}
		ag, err := x.Resolve(ctx, rest)
		return x, ag, err
	}
	x, err := e.API()
	if err != nil {
		return nil, nil, err
	}
	ag, firstErr := x.Resolve(ctx, ref)
	if firstErr == nil || e.profile != "" || os.Getenv("GB_PROFILE") != "" {
		return x, ag, firstErr
	}
	type hit struct {
		x  *api.API
		ag *api.Agent
	}
	var hits []hit
	for _, name := range config.List() {
		if name == x.P.Name {
			continue
		}
		y, err := e.apiFor(name)
		if err != nil {
			continue
		}
		if a, err := y.Resolve(ctx, ref); err == nil {
			hits = append(hits, hit{y, a})
		}
	}
	switch len(hits) {
	case 0:
		return nil, nil, firstErr
	case 1:
		return hits[0].x, hits[0].ag, nil
	}
	names := []string{}
	for _, h := range hits {
		names = append(names, h.x.P.Name+":"+h.ag.Name)
	}
	return nil, nil, fmt.Errorf("%q exists in several accounts: %s — prefix it with the account", ref, strings.Join(names, ", "))
}

type accountInfo struct {
	Name        string `json:"name"`
	Default     bool   `json:"default"`
	LoggedIn    bool   `json:"loggedIn"`
	Email       any    `json:"email,omitempty"`
	TokenExpiry string `json:"accessTokenExpires,omitempty"`
	Box         string `json:"box,omitempty"`
	Bots        *int   `json:"bots,omitempty"`
	Error       string `json:"error,omitempty"`
}

func cmdAccounts(ctx context.Context, e *env) error {
	sub := "list"
	if len(e.a.pos) > 0 {
		sub, e.a.pos = e.a.pos[0], e.a.pos[1:]
	}
	switch sub {
	case "list", "ls":
		names := config.List()
		def := config.DefaultName()
		out := make([]accountInfo, len(names))
		var wg sync.WaitGroup
		for i, name := range names {
			wg.Add(1)
			go func(i int, name string) { // accounts are independent: check them in parallel
				defer wg.Done()
				p, _ := config.Open(name)
				info := whoInfo(p)
				ai := accountInfo{Name: name, Default: name == def, LoggedIn: info["loggedIn"] == true, Email: info["email"]}
				if v, ok := info["accessTokenExpires"].(string); ok {
					ai.TokenExpiry = v
				}
				if ai.LoggedIn && e.a.bool("check") {
					cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
					if _, r, err := box.Ensure(cctx, p, false); err != nil {
						ai.Error = err.Error()
					} else {
						ai.Box = strings.TrimPrefix(r.RunState, "SAND_BOX_RUN_STATE_")
						var n float64
						if err := box.New(p).Call(cctx, "countAgents", nil, &n); err == nil {
							c := int(n)
							ai.Bots = &c
						}
					}
				}
				out[i] = ai
			}(i, name)
		}
		wg.Wait()
		e.emit(out, func(w io.Writer) {
			if len(out) == 0 {
				fmt.Fprintln(w, "No accounts. Run `gb accounts add <name>`.")
				return
			}
			rows := [][]string{}
			for _, a := range out {
				mark := " "
				if a.Default {
					mark = "*"
				}
				state := "logged out"
				if a.LoggedIn {
					state = "ok until " + strings.Split(a.TokenExpiry, "T")[0]
				}
				extra := a.Box
				if a.Bots != nil {
					extra += fmt.Sprintf(" · %d bots", *a.Bots)
				}
				if a.Error != "" {
					extra = "error: " + trunc(a.Error, 50)
				}
				rows = append(rows, []string{mark + a.Name, fmt.Sprint(a.Email), state, extra})
			}
			table(w, []string{" ACCOUNT", "USER", "TOKEN", "BOX"}, rows)
			if !e.a.bool("check") {
				fmt.Fprintln(w, "(--check also shows box state and bot counts)")
			}
		})
		return nil

	case "add", "login":
		if len(e.a.pos) == 0 {
			return usage("gb accounts add <name> [--api-key KEY]")
		}
		if err := config.ValidName(e.a.pos[0]); err != nil {
			return err
		}
		first := len(config.List()) == 0
		e.profile = e.a.pos[0]
		if err := cmdLogin(ctx, e); err != nil {
			return err
		}
		if first {
			return config.SetDefault(e.profile)
		}
		return nil

	case "remove", "rm", "logout":
		if len(e.a.pos) == 0 {
			return usage("gb accounts remove <name>")
		}
		name := e.a.pos[0]
		if !config.Exists(name) {
			return fmt.Errorf("no account %q", name)
		}
		p, _ := config.Open(name)
		if _, err := os.Stat(p.Dir + "/gbd.sock"); err == nil {
			return fmt.Errorf("gbd is running for %q; stop it first", name)
		}
		// Revoking the server session would also log out any other profile signed in as the same user
		// (same refresh token family), so only revoke when no other profile shares the user.
		revoked, shared := false, ""
		if c, _ := p.Credentials(); c != nil && c.AccessToken != "" && !e.a.bool("keep-session") {
			if shared = sharedWith(name, c.AccessToken); shared == "" {
				revoked = auth.Logout(ctx, c.AccessToken) == nil
			}
		}
		if err := config.Delete(name); err != nil {
			return err
		}
		if config.DefaultName() == name {
			if rest := config.List(); len(rest) > 0 {
				_ = config.SetDefault(rest[0])
			} else {
				_ = os.Remove(config.Root() + "/default-profile")
			}
		}
		e.emit(map[string]any{"removed": name, "default": config.DefaultName(), "serverRevoked": revoked, "sharedWith": shared}, func(w io.Writer) {
			fmt.Fprintf(w, "Removed account %s (default is now %s)\n", name, config.DefaultName())
			if shared != "" {
				fmt.Fprintf(w, "note: kept the server session because %q is signed in as the same user\n", shared)
			}
		})
		return nil

	case "default", "use":
		if len(e.a.pos) == 0 {
			fmt.Fprintln(e.out, config.DefaultName())
			return nil
		}
		if !config.Exists(e.a.pos[0]) {
			return fmt.Errorf("no account %q (gb accounts list)", e.a.pos[0])
		}
		if err := config.SetDefault(e.a.pos[0]); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Default account: %s\n", e.a.pos[0])
		return nil
	}
	return usage("gb accounts list [--check] | add <name> | remove <name> | default [name]")
}

// allAccountsAgents lists bots/groups of every account in parallel.
func (e *env) allAccountsAgents(ctx context.Context) (map[string][]api.Agent, []string, error) {
	names := config.List()
	if len(names) == 0 {
		return nil, nil, errors.New("no accounts; run `gb accounts add <name>`")
	}
	out := map[string][]api.Agent{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	for _, n := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			x, err := e.apiFor(n)
			var list []api.Agent
			if err == nil {
				list, err = x.Agents(ctx, true)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", n, err)
				}
				return
			}
			out[n] = list
		}(n)
	}
	wg.Wait()
	sort.Strings(names)
	if len(out) == 0 {
		return nil, nil, firstErr
	}
	if firstErr != nil {
		fmt.Fprintln(os.Stderr, "warning:", firstErr)
	}
	return out, names, nil
}

// sharedWith returns another account profile signed in as the same user (JWT sub) as token, if any.
// Revoking the session server-side would log that profile out too.
func sharedWith(name, token string) string {
	sub, _ := auth.Claims(token)["sub"].(string)
	if sub == "" {
		return ""
	}
	for _, other := range config.List() {
		if other == name {
			continue
		}
		q, _ := config.Open(other)
		if oc, _ := q.Credentials(); oc != nil {
			if osub, _ := auth.Claims(oc.AccessToken)["sub"].(string); osub == sub {
				return other
			}
		}
	}
	return ""
}
