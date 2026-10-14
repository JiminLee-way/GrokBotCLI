package cli

// App connections (MCP connectors / plugins), listener integrations and box secrets.
//
// OAuth on a headless server: authenticateMcpServer returns an authorization URL whose final redirect is
// http://localhost:<port>/callback?code&state. The code must be handed back with completeMcpOAuth. We take
// whichever arrives first: a loopback listener on this machine (works locally or via `ssh -L`), the user
// pasting the failed localhost URL from the browser, or the connector turning "connected" on its own.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gb/internal/api"
	"gb/internal/box"
)

type mcpServer struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	PluginID           string `json:"pluginId"`
	ServerIdentifier   string `json:"serverIdentifier"`
	AccountKey         string `json:"accountKey"`
	Status             string `json:"status"`
	Transport          string `json:"transport"`
	ToolCount          int    `json:"toolCount"`
	IsTeamServer       bool   `json:"isTeamServer"`
	CustomInstructions string `json:"customInstructions"`
	URL                string `json:"url"`
}

type catalogEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Publisher   struct {
		DisplayName string `json:"displayName"`
	} `json:"publisher"`
	PopularityRank int `json:"popularityRank"`
	Fields         []struct {
		Key        string `json:"key"`
		Label      string `json:"label"`
		IsRequired bool   `json:"isRequired"`
		IsSecret   bool   `json:"isSecret"`
		Hint       string `json:"hint"`
	} `json:"fields"`
}

func mcpState(ctx context.Context, x *api.API) ([]mcpServer, error) {
	var st struct {
		Servers []mcpServer `json:"servers"`
	}
	return st.Servers, x.Call(ctx, "getMcpState", nil, &st)
}

func mcpCatalog(ctx context.Context, x *api.API) ([]catalogEntry, error) {
	var c []catalogEntry
	return c, x.Call(ctx, "getMcpCatalog", nil, &c)
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// findServers returns connected/installed rows for an app reference (name, plugin id, server id).
func findServers(rows []mcpServer, ref string) []mcpServer {
	r := lower(ref)
	var out []mcpServer
	for _, s := range rows {
		if lower(s.Name) == r || lower(s.PluginID) == r || lower(s.ServerIdentifier) == r || s.ID == ref || strings.HasPrefix(s.ID, ref) {
			out = append(out, s)
		}
	}
	return out
}

func findEntry(cat []catalogEntry, ref string) (*catalogEntry, error) {
	r := lower(ref)
	var hits []*catalogEntry
	for i := range cat {
		c := &cat[i]
		if lower(c.ID) == r || lower(c.Name) == r || lower(c.DisplayName) == r {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, fmt.Errorf("no app %q in the catalog (gb apps catalog %s)", ref, ref)
	}
	ids := []string{}
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	return nil, fmt.Errorf("%q is ambiguous: %s — use the id", ref, strings.Join(ids, ", "))
}

func pickServer(rows []mcpServer, account string) (*mcpServer, error) {
	if len(rows) == 0 {
		return nil, errors.New("not installed")
	}
	for i := range rows {
		if account != "" && rows[i].AccountKey == account {
			return &rows[i], nil
		}
	}
	if account != "" {
		// A new account key adds a second account on the same server.
		s := rows[0]
		s.AccountKey, s.Status = account, "new-account"
		return &s, nil
	}
	return &rows[0], nil
}

func cmdApps(ctx context.Context, e *env) error {
	sub := "list"
	if len(e.a.pos) > 0 {
		sub, e.a.pos = e.a.pos[0], e.a.pos[1:]
	}
	x, err := e.API()
	if err != nil {
		return err
	}
	needApp := func() (string, error) {
		if len(e.a.pos) == 0 {
			return "", usage("gb apps %s <app>", sub)
		}
		return e.a.pos[0], nil
	}
	switch sub {
	case "list", "ls":
		rows, err := mcpState(ctx, x)
		if err != nil {
			return err
		}
		sort.Slice(rows, func(i, j int) bool { return lower(rows[i].Name) < lower(rows[j].Name) })
		e.emit(rows, func(w io.Writer) {
			if len(rows) == 0 {
				fmt.Fprintln(w, "No apps connected. Browse with `gb apps catalog <query>`.")
				return
			}
			t := [][]string{}
			for _, s := range rows {
				scope := ""
				if s.IsTeamServer {
					scope = "team"
				}
				t = append(t, []string{s.Name, s.Status, s.AccountKey, strconv.Itoa(s.ToolCount), s.Transport, scope, short(s.ID)})
			}
			table(w, []string{"APP", "STATUS", "ACCOUNT", "TOOLS", "TRANSPORT", "SCOPE", "ID"}, t)
		})
		return nil

	case "catalog", "search":
		cat, err := mcpCatalog(ctx, x)
		if err != nil {
			return err
		}
		q := lower(strings.Join(e.a.pos, " "))
		var out []catalogEntry
		for _, c := range cat {
			if q == "" || strings.Contains(lower(c.Name+" "+c.DisplayName+" "+c.Description+" "+c.Category+" "+c.ID), q) {
				out = append(out, c)
			}
		}
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i].PopularityRank, out[j].PopularityRank
			if a == 0 {
				a = 1 << 30
			}
			if b == 0 {
				b = 1 << 30
			}
			return a < b
		})
		if n := e.a.int("limit", 30); len(out) > n {
			out = out[:n]
		}
		e.emit(out, func(w io.Writer) {
			t := [][]string{}
			for _, c := range out {
				req := []string{}
				for _, f := range c.Fields {
					if f.IsRequired {
						req = append(req, f.Key)
					}
				}
				t = append(t, []string{c.ID, firstOr(c.DisplayName, c.Name), c.Category, strings.Join(req, ","), trunc(c.Description, 50)})
			}
			table(w, []string{"ID", "NAME", "CATEGORY", "REQUIRED", "DESCRIPTION"}, t)
		})
		return nil

	case "connect", "add":
		ref, err := needApp()
		if err != nil {
			return err
		}
		return appsConnect(ctx, e, x, ref)

	case "disconnect", "remove", "rm":
		ref, err := needApp()
		if err != nil {
			return err
		}
		rows, err := mcpState(ctx, x)
		if err != nil {
			return err
		}
		hits := findServers(rows, ref)
		if len(hits) == 0 {
			return fmt.Errorf("%q is not connected (gb apps list)", ref)
		}
		var res any
		if e.a.bool("all") { // remove the whole connector (all accounts); plugins uninstall too
			var r struct {
				Removed bool   `json:"removed"`
				Reason  string `json:"reason"`
			}
			if err := x.Call(ctx, "removeMcpServer", map[string]any{"serverId": hits[0].ID}, &r); err != nil {
				return err
			}
			if !r.Removed && hits[0].PluginID != "" {
				if err := x.Call(ctx, "uninstallMcpPlugin", map[string]any{"pluginId": hits[0].PluginID}, &r); err != nil {
					return err
				}
			}
			if !r.Removed {
				return fmt.Errorf("not removed (%s)", firstOr(r.Reason, "unknown"))
			}
			res = r
		} else {
			s, err := pickServer(hits, e.a.get("account", ""))
			if err != nil {
				return err
			}
			if err := x.Call(ctx, "removeMcpAccount", map[string]any{"serverId": s.ID, "accountKey": s.AccountKey}, nil); err != nil {
				return err
			}
			res = map[string]any{"removed": true, "account": s.AccountKey}
		}
		e.emit(res, func(w io.Writer) { fmt.Fprintf(w, "Disconnected %s\n", hits[0].Name) })
		return nil

	case "tools":
		ref, err := needApp()
		if err != nil {
			return err
		}
		rows, err := mcpState(ctx, x)
		if err != nil {
			return err
		}
		hits := findServers(rows, ref)
		if len(hits) == 0 {
			return fmt.Errorf("%q is not connected", ref)
		}
		id := hits[0].ID
		type tool struct {
			Name       string `json:"name"`
			Title      string `json:"title"`
			IsDisabled bool   `json:"isDisabled"`
			Permission string `json:"permission"`
			IsReadOnly *bool  `json:"isReadOnly"`
		}
		var tools []tool
		if err := x.Call(ctx, "listMcpServerTools", map[string]any{"serverId": id}, &tools); err != nil {
			return err
		}
		// toggleMcpToolDisabled flips, so only call it when the state actually differs.
		want := map[string]bool{}
		for _, t := range e.a.list("disable") {
			want[t] = true
		}
		for _, t := range e.a.list("enable") {
			want[t] = false
		}
		for name, disable := range want {
			found := false
			for _, t := range tools {
				if t.Name == name {
					found = true
					if t.IsDisabled != disable {
						if err := x.Call(ctx, "toggleMcpToolDisabled", map[string]any{"serverId": id, "toolName": name}, &tools); err != nil {
							return err
						}
					}
				}
			}
			if !found {
				return fmt.Errorf("%s has no tool %q", hits[0].Name, name)
			}
		}
		e.emit(tools, func(w io.Writer) {
			t := [][]string{}
			for _, x := range tools {
				state := "on"
				if x.IsDisabled {
					state = "off"
				}
				ro := ""
				if x.IsReadOnly != nil && *x.IsReadOnly {
					ro = "read-only"
				}
				t = append(t, []string{x.Name, state, firstOr(x.Permission, "-"), ro})
			}
			table(w, []string{"TOOL", "STATE", "PERMISSION", ""}, t)
		})
		return nil

	case "instructions":
		ref, err := needApp()
		if err != nil {
			return err
		}
		rows, err := mcpState(ctx, x)
		if err != nil {
			return err
		}
		hits := findServers(rows, ref)
		if len(hits) == 0 {
			return fmt.Errorf("%q is not connected", ref)
		}
		if len(e.a.pos) < 2 {
			e.emit(map[string]any{"app": hits[0].Name, "instructions": hits[0].CustomInstructions}, func(w io.Writer) {
				fmt.Fprintln(w, firstOr(hits[0].CustomInstructions, "(no custom instructions)"))
			})
			return nil
		}
		text, err := readMessage(e.a.pos[1:])
		if err != nil && !e.a.bool("clear") {
			return err
		}
		if e.a.bool("clear") {
			text = ""
		}
		if err := x.Call(ctx, "setMcpCustomInstructions", map[string]any{"serverId": hits[0].ID, "instructions": text}, nil); err != nil {
			return err
		}
		e.emit(map[string]any{"app": hits[0].Name, "instructions": text}, func(w io.Writer) { fmt.Fprintf(w, "Updated instructions for %s\n", hits[0].Name) })
		return nil

	case "rename-account":
		if len(e.a.pos) < 3 {
			return usage("gb apps rename-account <app> <old-account> <new-account>")
		}
		rows, err := mcpState(ctx, x)
		if err != nil {
			return err
		}
		hits := findServers(rows, e.a.pos[0])
		if len(hits) == 0 {
			return fmt.Errorf("%q is not connected", e.a.pos[0])
		}
		if err := x.Call(ctx, "renameMcpAccount", map[string]any{"serverId": hits[0].ID, "accountKey": e.a.pos[1], "newAccountKey": e.a.pos[2]}, nil); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Renamed %s account %s → %s\n", hits[0].Name, e.a.pos[1], e.a.pos[2])
		return nil
	}
	return usage("gb apps list | catalog [query] | connect <app> | disconnect <app> [--all] | tools <app> [--enable T] [--disable T] | instructions <app> [text|--clear] | rename-account")
}

func appsConnect(ctx context.Context, e *env, x *api.API, ref string) error {
	rows, err := mcpState(ctx, x)
	if err != nil {
		return err
	}
	hits := findServers(rows, ref)
	if len(hits) == 0 { // install from the catalog first
		cat, err := mcpCatalog(ctx, x)
		if err != nil {
			return err
		}
		entry, err := findEntry(cat, ref)
		if err != nil {
			return err
		}
		values := map[string]any{}
		for _, kv := range e.a.list("set") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return usage("--set takes KEY=VALUE")
			}
			if strings.HasPrefix(v, "env:") { // keep secrets out of shell history: --set API_KEY=env:MY_VAR
				v = os.Getenv(strings.TrimPrefix(v, "env:"))
			}
			values[k] = v
		}
		var missing []string
		for _, f := range entry.Fields {
			if _, ok := values[f.Key]; f.IsRequired && !ok {
				missing = append(missing, fmt.Sprintf("--set %s=… (%s)", f.Key, firstOr(f.Label, f.Hint)))
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("%s needs: %s", firstOr(entry.DisplayName, entry.Name), strings.Join(missing, ", "))
		}
		args := map[string]any{"entryId": entry.ID}
		if len(values) > 0 {
			args["values"] = values
		}
		var st struct {
			Servers []mcpServer `json:"servers"`
		}
		if err := x.Call(ctx, "installMcpEntry", args, &st); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Installed %s\n", firstOr(entry.DisplayName, entry.Name))
		if hits = findServers(st.Servers, entry.ID); len(hits) == 0 {
			hits = findServers(st.Servers, entry.Name)
		}
		if len(hits) == 0 {
			rows, _ = mcpState(ctx, x)
			hits = findServers(rows, entry.Name)
		}
		if len(hits) == 0 {
			e.emit(map[string]any{"installed": entry.ID}, func(w io.Writer) { fmt.Fprintln(w, "Installed; it needs no sign-in.") })
			return nil
		}
	}
	s, err := pickServer(hits, e.a.get("account", ""))
	if err != nil {
		return err
	}
	if s.Status == "connected" && !e.a.bool("reauth") {
		e.emit(s, func(w io.Writer) {
			fmt.Fprintf(w, "%s is already connected (account %s). Use --reauth to sign in again.\n", s.Name, s.AccountKey)
		})
		return nil
	}
	port := e.a.int("port", 8787)
	args := map[string]any{"serverId": s.ID, "accountKey": firstOr(s.AccountKey, "default"), "oauthRedirectUri": fmt.Sprintf("http://localhost:%d/callback", port)}
	if e.a.bool("reauth") {
		args["forceReauth"] = true
	}
	var r struct {
		Status           string `json:"status"`
		AuthorizationURL string `json:"authorizationUrl"`
		ServerName       string `json:"serverName"`
		Reason           *struct {
			Kind string `json:"kind"`
		} `json:"reason"`
	}
	if err := x.Call(ctx, "authenticateMcpServer", args, &r); err != nil {
		return err
	}
	switch r.Status {
	case "already-authenticated":
		fmt.Fprintf(e.out, "%s is already connected.\n", firstOr(r.ServerName, s.Name))
		return nil
	case "not-supported":
		kind := ""
		if r.Reason != nil {
			kind = r.Reason.Kind
		}
		hint := map[string]string{
			"admin_blocked":     "your team admin has disabled this app",
			"grant_required":    "a bot must request it first; approve the grant card (gb approvals list)",
			"stdio_unsupported": "it runs as a local process; register a computer for the bot to run it",
			"no_auth_link":      "the app did not return a sign-in link",
		}[kind]
		return fmt.Errorf("%s cannot be connected here: %s %s", s.Name, kind, hint)
	case "started":
	default:
		return fmt.Errorf("%s: %s", s.Name, r.Status)
	}
	return waitOAuth(ctx, e, x, s, r.AuthorizationURL, port)
}

// waitOAuth prints the sign-in URL and completes it from the first available source.
func waitOAuth(ctx context.Context, e *env, x *api.API, s *mcpServer, authURL string, port int) error {
	if e.json {
		e.emit(map[string]any{"status": "pending", "authorizationUrl": authURL}, nil)
	} else {
		fmt.Fprintf(os.Stderr, "Open this URL in a browser and sign in to %s:\n\n  %s\n\n", s.Name, authURL)
		fmt.Fprintf(os.Stderr, "After signing in the browser goes to http://localhost:%d/callback…\n", port)
		fmt.Fprintf(os.Stderr, "  • same machine or `ssh -L %d:localhost:%d <server>`: it finishes automatically\n", port, port)
		fmt.Fprintf(os.Stderr, "  • otherwise the page fails to load — copy that URL from the address bar and paste it here\n\nWaiting… ")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	type cb struct{ code, state, err string }
	got := make(chan cb, 4)

	// 1) loopback listener
	if !e.a.bool("no-listen") {
		mux := http.NewServeMux()
		mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			got <- cb{q.Get("code"), q.Get("state"), q.Get("error")}
			fmt.Fprintln(w, "Connected. You can close this tab and return to the terminal.")
		})
		for _, addr := range []string{fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("[::1]:%d", port)} {
			if ln, err := net.Listen("tcp", addr); err == nil {
				srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
				go srv.Serve(ln)
				defer srv.Close()
			}
		}
	}
	// 2) pasted callback URL
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if u, err := url.Parse(line); err == nil && (u.Query().Get("code") != "" || u.Query().Get("error") != "") {
				q := u.Query()
				got <- cb{q.Get("code"), q.Get("state"), q.Get("error")}
				return
			}
			if line != "" {
				fmt.Fprintln(os.Stderr, "That does not look like the callback URL (it should contain ?code=…&state=…). Try again:")
			}
		}
	}()
	// 3) the connector becomes connected some other way
	connected := func() bool {
		rows, err := mcpState(ctx, x)
		if err != nil {
			return false
		}
		for _, r := range rows {
			if r.ID == s.ID && r.AccountKey == firstOr(s.AccountKey, "default") && r.Status == "connected" {
				return true
			}
		}
		return false
	}
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.New("timed out waiting for the sign-in")
		case c := <-got:
			if c.err != "" {
				return fmt.Errorf("sign-in failed: %s", c.err)
			}
			if err := x.Call(ctx, "completeMcpOAuth", map[string]any{"code": c.code, "state": c.state}, nil); err != nil {
				return err
			}
			for i := 0; i < 10 && !connected(); i++ {
				time.Sleep(time.Second)
			}
			return connectedDone(ctx, e, x, s)
		case <-tick.C:
			if connected() {
				return connectedDone(ctx, e, x, s)
			}
		}
	}
}

func connectedDone(ctx context.Context, e *env, x *api.API, s *mcpServer) error {
	_ = x.Call(ctx, "syncPluginSkills", nil, nil) // pick up skills bundled with the plugin
	e.emit(map[string]any{"status": "connected", "app": s.Name, "account": firstOr(s.AccountKey, "default")}, func(w io.Writer) {
		fmt.Fprintf(w, "\nConnected %s (account %s).\n", s.Name, firstOr(s.AccountKey, "default"))
	})
	return nil
}

// ---------- listener integrations ----------

func cmdListeners(ctx context.Context, e *env) error {
	sub := "list"
	if len(e.a.pos) > 0 {
		sub, e.a.pos = e.a.pos[0], e.a.pos[1:]
	}
	x, err := e.API()
	if err != nil {
		return err
	}
	type integ struct {
		Platform      string `json:"platform"`
		State         string `json:"state"`
		IsConnected   bool   `json:"isConnected"`
		NeededByCount int    `json:"neededByCount"`
	}
	list := func() ([]integ, error) {
		var r struct {
			Integrations []integ `json:"integrations"`
		}
		return r.Integrations, x.Call(ctx, "getListenerIntegrations", nil, &r)
	}
	isConnected := func(p string) bool {
		l, err := list()
		if err != nil {
			return false
		}
		for _, i := range l {
			if i.Platform == p {
				return i.IsConnected
			}
		}
		return false
	}
	switch sub {
	case "list", "ls":
		l, err := list()
		if err != nil {
			return err
		}
		e.emit(l, func(w io.Writer) {
			t := [][]string{}
			for _, i := range l {
				t = append(t, []string{i.Platform, i.State, strconv.Itoa(i.NeededByCount)})
			}
			table(w, []string{"PLATFORM", "STATE", "NEEDED BY"}, t)
		})
		return nil
	case "connect":
		if len(e.a.pos) == 0 {
			return usage("gb listeners connect <github|gitlab|bitbucket|azure-devops|origin|slack>")
		}
		p := e.a.pos[0]
		var link string
		if p == "slack" {
			var r struct {
				URL string `json:"url"`
			}
			if err := box.Backend(ctx, x.P, "aiserver.v1.DashboardService/GetSlackInstallUrl", map[string]any{}, &r); err != nil {
				return err
			}
			link = r.URL
		} else {
			var r struct {
				URL string `json:"url"`
			}
			args := map[string]any{"platform": p}
			if e.a.bool("reauth") {
				args["forceOauth"] = true
			}
			if err := x.Call(ctx, "getListenerConnectUrl", args, &r); err != nil {
				return err
			}
			link = r.URL
		}
		fmt.Fprintf(os.Stderr, "Open this URL and approve:\n\n  %s\n\n", link)
		codeHost := p == "github" || p == "gitlab" || p == "bitbucket" || p == "azure-devops"
		if codeHost {
			fmt.Fprintln(os.Stderr, "When the browser ends on a grokbot://… link that does not open, copy it and paste it here.")
		}
		fmt.Fprint(os.Stderr, "Waiting… ")
		pasted := make(chan url.Values, 1)
		go func() {
			sc := bufio.NewScanner(os.Stdin)
			for sc.Scan() {
				if u, err := url.Parse(strings.TrimSpace(sc.Text())); err == nil && u.Query().Get("state") != "" {
					pasted <- u.Query()
					return
				}
			}
		}()
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		tick := time.NewTicker(4 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return errors.New("timed out")
			case q := <-pasted:
				args := map[string]any{"state": q.Get("state")}
				for _, k := range []string{"code", "installationId", "setupAction", "error"} {
					if v := q.Get(k); v != "" {
						args[k] = v
					}
				}
				if v := q.Get("installation_id"); v != "" {
					args["installationId"] = v
				}
				if v := q.Get("setup_action"); v != "" {
					args["setupAction"] = v
				}
				var r struct {
					Outcome string `json:"outcome"`
				}
				if err := x.Call(ctx, "completeGithubConnect", args, &r); err != nil {
					return err
				}
				msg := map[string]string{"connected": "Connected.", "pending_approval": "Waiting for an organization admin to approve.", "failed": "Failed.", "stale": "The link expired; run connect again."}[r.Outcome]
				e.emit(r, func(w io.Writer) { fmt.Fprintf(w, "\n%s %s\n", p, msg) })
				return nil
			case <-tick.C:
				if isConnected(p) {
					e.emit(map[string]any{"platform": p, "connected": true}, func(w io.Writer) { fmt.Fprintf(w, "\n%s connected.\n", p) })
					return nil
				}
			}
		}
	case "disconnect":
		if len(e.a.pos) == 0 {
			return usage("gb listeners disconnect <platform>")
		}
		if err := x.Call(ctx, "disconnectListenerPlatform", map[string]any{"platform": e.a.pos[0]}, nil); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Disconnected %s\n", e.a.pos[0])
		return nil
	}
	return usage("gb listeners list | connect <platform> | disconnect <platform>")
}

// ---------- box secrets (environment variables available to bots on the box) ----------

// setBoxSecrets replaces the whole map and values cannot be read back, so gb keeps the values it set in
// secrets.json (0600) and refuses to overwrite keys it does not know unless --force.
func cmdSecrets(ctx context.Context, e *env) error {
	sub := "list"
	if len(e.a.pos) > 0 {
		sub, e.a.pos = e.a.pos[0], e.a.pos[1:]
	}
	x, err := e.API()
	if err != nil {
		return err
	}
	var st struct {
		Keys            []string `json:"keys"`
		IsApplied       bool     `json:"isApplied"`
		LastAppliedAtMs float64  `json:"lastAppliedAtMs"`
	}
	if err := x.Call(ctx, "getBoxSecretsStatus", nil, &st); err != nil {
		return err
	}
	local := map[string]string{}
	_ = x.P.ReadJSON("secrets.json", &local)
	switch sub {
	case "list", "ls":
		e.emit(st, func(w io.Writer) {
			for _, k := range st.Keys {
				origin := "set elsewhere (value unknown to gb)"
				if _, ok := local[k]; ok {
					origin = "set by gb"
				}
				fmt.Fprintf(w, "%-30s %s\n", k, origin)
			}
			fmt.Fprintf(w, "applied=%v\n", st.IsApplied)
		})
		return nil
	case "set", "unset":
		if len(e.a.pos) == 0 {
			return usage("gb secrets set KEY=VALUE [KEY=env:VAR ...] | unset KEY ...")
		}
		var unknown []string
		for _, k := range st.Keys {
			if _, ok := local[k]; !ok {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 && !e.a.bool("force") {
			return fmt.Errorf("the box has secrets gb does not know (%s); setting replaces the whole set and would drop them. Re-add them with `gb secrets set` or pass --force", strings.Join(unknown, ", "))
		}
		for _, kv := range e.a.pos {
			if sub == "unset" {
				delete(local, kv)
				continue
			}
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return usage("set takes KEY=VALUE")
			}
			if strings.HasPrefix(v, "env:") {
				v = os.Getenv(strings.TrimPrefix(v, "env:"))
			}
			local[k] = v
		}
		if err := x.Call(ctx, "setBoxSecrets", map[string]any{"secrets": local}, nil); err != nil {
			return err
		}
		if err := x.P.WriteJSON("secrets.json", local); err != nil {
			return err
		}
		keys := []string{}
		for k := range local {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b, _ := json.Marshal(keys)
		fmt.Fprintf(e.out, "Box secrets now: %s\n", b)
		return nil
	}
	return usage("gb secrets list | set KEY=VALUE | unset KEY")
}
