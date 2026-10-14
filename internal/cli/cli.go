// Package cli implements the `gb` command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"gb/internal/api"
	"gb/internal/box"
)

var Version = "0.1.0"

// args is a tiny flag parser that accepts flags anywhere: --k v, --k=v, -k v, repeated flags, and "--" to stop.
type args struct {
	pos   []string
	flags map[string][]string
}

var boolFlags = map[string]bool{
	"json": true, "help": true, "h": true, "all": true, "show-tools": true, "approvals": true,
	"wake": true, "all-accounts": true, "A": true, "check": true, "keep-session": true, "reauth": true, "no-listen": true, "force": true, "clear": true, "dismiss": true, "always": true, "never": true, "webhook": true, "exact": true, "off": true, "fail": true, "send": true, "no-browser": true, "groups": true, "quiet": true, "q": true, "fresh": true, "raw": true,
}

var shortAlias = map[string]string{"n": "limit", "h": "help", "q": "quiet", "d": "description", "m": "member", "f": "file", "t": "timeout", "o": "output"}

func parse(in []string) *args {
	a := &args{flags: map[string][]string{}}
	for i := 0; i < len(in); i++ {
		s := in[i]
		if s == "--" {
			a.pos = append(a.pos, in[i+1:]...)
			break
		}
		if !strings.HasPrefix(s, "-") || s == "-" || isNumber(s) {
			a.pos = append(a.pos, s)
			continue
		}
		name := strings.TrimLeft(s, "-")
		val, hasVal := "", false
		if k, v, ok := strings.Cut(name, "="); ok {
			name, val, hasVal = k, v, true
		}
		if full, ok := shortAlias[name]; ok && !strings.HasPrefix(s, "--") {
			name = full
		}
		if boolFlags[name] {
			if !hasVal {
				val = "true"
			}
		} else if !hasVal {
			if i+1 < len(in) {
				i++
				val = in[i]
			}
		}
		a.flags[name] = append(a.flags[name], val)
	}
	return a
}

func isNumber(s string) bool { _, err := strconv.ParseFloat(s, 64); return err == nil }

func (a *args) str(name string) (string, bool) {
	v, ok := a.flags[name]
	if !ok {
		return "", false
	}
	return v[len(v)-1], true
}
func (a *args) get(name, def string) string {
	if v, ok := a.str(name); ok {
		return v
	}
	return def
}
func (a *args) list(name string) []string { return a.flags[name] }
func (a *args) bool(name string) bool {
	v, ok := a.str(name)
	return ok && v != "false" && v != "off" && v != "0"
}
func (a *args) int(name string, def int) int {
	if v, ok := a.str(name); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// onOff parses on/off/true/false/yes/no into *bool (nil when the flag is absent).
func (a *args) onOff(name string) (*bool, error) {
	v, ok := a.str(name)
	if !ok {
		return nil, nil
	}
	switch strings.ToLower(v) {
	case "on", "true", "yes", "1":
		t := true
		return &t, nil
	case "off", "false", "no", "0":
		f := false
		return &f, nil
	}
	return nil, fmt.Errorf("--%s takes on|off", name)
}

func (a *args) optStr(name string) *string {
	if v, ok := a.str(name); ok {
		return &v
	}
	return nil
}

// ---------- output ----------

type env struct {
	a       *args
	json    bool
	profile string // explicit --profile/--account; "" = default account
	out     io.Writer

	mu   sync.Mutex
	apis map[string]*api.API
}

func (e *env) emit(v any, human func(w io.Writer)) {
	if e.json {
		enc := json.NewEncoder(e.out)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		return
	}
	human(e.out)
}

func table(w io.Writer, header []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func trunc(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// usageError exits with code 2.
type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

func usage(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// exitError carries a specific exit code.
type exitError struct {
	code int
	err  error
}

func (x exitError) Error() string { return x.err.Error() }

// ---------- entry ----------

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, e *env) error
}

func commands() []command {
	return []command{
		{"login", "Log in (prints a URL to approve in any browser) · --api-key KEY", cmdLogin},
		{"logout", "Revoke the session and delete local credentials", cmdLogout},
		{"accounts", "accounts list [--check] | add <name> | remove <name> | default [name] — multiple Grok Bot accounts", cmdAccounts},
		{"whoami", "Show the logged-in account and token expiry", cmdWhoami},
		{"doctor", "Check credentials, box state, gateway health and event stream", cmdDoctor},
		{"box", "box status | box wake", cmdBox},
		{"bots", "bots list [--all-accounts] | get | create | update | delete | dup", cmdBots},
		{"groups", "groups list|get|create|update|delete|members|add|remove|set|people|invite|kick|leave", cmdGroups},
		{"people", "people [--team ID] — Cursor team members you can invite to group chats", cmdPeople},
		{"send", "send <bot|group> <message|-> [--reply-to ID] [--nonce N] [--wait]", cmdSend},
		{"ask", "ask <bot|group> <message|-> — send and print the reply when the bot finishes", cmdAsk},
		{"log", "log <bot|group> [-n 30] [--before SEQ] [--after ENTRY_ID] — conversation history", cmdLog},
		{"thread", "thread <bot|group> <rootEntryId> — one reply thread", cmdThread},
		{"watch", "watch [bot|group ...] [--approvals] — live stream of new messages", cmdWatch},
		{"events", "events [--channel NAME] — raw gateway event stream as NDJSON (debugging)", cmdEvents},
		{"search", "search <query> [-n 20] — search messages across all bots", cmdSearch},
		{"stop", "stop <bot> — interrupt the running turn", cmdStop},
		{"reset", "reset <bot> — start fresh after a failed turn (the app's 'Start fresh')", cmdReset},
		{"undo", "undo <bot> — remove a blocked turn and its message (the app's 'Roll back')", cmdUndo},
		{"skills", "skills list | add <SKILL.md|dir|url> | remove <id|name>", cmdSkills},
		{"apps", "apps list | catalog [q] | connect <app> | disconnect <app> | tools <app> | instructions <app> — app connections (MCP/plugins)", cmdApps},
		{"listeners", "listeners list | connect <platform> | disconnect <platform> — GitHub/GitLab/Slack/... triggers", cmdListeners},
		{"secrets", "secrets list | set KEY=VALUE | unset KEY — environment secrets on the box", cmdSecrets},
		{"pending", "pending [bot] [--all] — everything waiting for you: approvals, questions, forms, secrets, app access", cmdPending},
		{"approvals", "approvals list [bot] | approve|deny <bot> <entryId> [--always|--never] [--command TEXT]", cmdApprovals},
		{"answer", "answer <bot> <entryId> <option...> [--custom TEXT] [--dismiss] — answer a bot's question", cmdAnswer},
		{"form", "form <bot> <entryId> id=value ... [--dismiss] — fill a form the bot asked for", cmdForm},
		{"secret", "secret <bot> <entryId> [env:VAR|-] — give the bot a secret it asked for (stdin by default)", cmdSecret},
		{"grant", "grant <bot> <entryId> allow|allow-bot|allow-team|skip — let a bot use a connected app", cmdGrant},
		{"files", "files list <bot> | get <bot> <path> [-o FILE] | search <query> — files bots made or received", cmdFiles},
		{"auto", "auto list [bot] | add <bot> --name N --prompt P --cron EXPR|--webhook | update|on|off|run|rm|webhook <bot> <id> — routines", cmdAuto},
		{"alerts", "alerts [--clear] | alerts dismiss <id> — usage limits, paused routines, failed syncs", cmdAlerts},
		{"call", "call <method> [json|-] — raw gateway call (see docs/GATEWAY_METHODS.md)", cmdCall},
		{"daemon", "daemon run|status|install [--system] — long-running gbd with local API + webhooks", cmdDaemon},
		{"rpc", "rpc <Service>/<Method> [json|-] — raw Cursor backend call (e.g. GrokBotService/ListGrokBotAgents)", cmdRPC},
		{"version", "Print the version", func(_ context.Context, e *env) error { fmt.Fprintln(e.out, "gb", Version); return nil }},
	}
}

func help(w io.Writer) {
	fmt.Fprintf(w, "gb %s — headless Grok Bot client\n\nUsage: gb <command> [args] [--json] [--account NAME]\n\nCommands:\n", Version)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, c := range commands() {
		fmt.Fprintf(tw, "  %s\t%s\n", c.name, c.summary)
	}
	tw.Flush()
	fmt.Fprint(w, `
Bots and groups are referenced by id, exact name, or unique name prefix; prefix with "account:" to pick an
account (e.g. work:"Researcher"). Unprefixed names are looked up in the default account, then in the others.
Env: GB_PROFILE, GB_HOME, CURSOR_ACCESS_TOKEN, GB_GATEWAY_URL/GB_GATEWAY_TOKEN/GB_GATEWAY_NETWORK_TOKEN, GB_DEBUG=1
`)
}

// Run executes the CLI and returns the process exit code.
func Run(argv []string) int {
	a := parse(argv)
	e := &env{a: a, json: a.bool("json"), profile: a.get("profile", a.get("account", "")), out: os.Stdout}
	if len(a.pos) == 0 || (a.bool("help") && len(a.pos) == 0) {
		help(os.Stdout)
		return 0
	}
	name := a.pos[0]
	a.pos = a.pos[1:]
	var cmd *command
	for _, c := range commands() {
		if c.name == name {
			c := c
			cmd = &c
		}
	}
	if name == "help" {
		help(os.Stdout)
		return 0
	}
	if cmd == nil {
		fmt.Fprintf(os.Stderr, "gb: unknown command %q (gb help)\n", name)
		return 2
	}
	if a.bool("help") {
		fmt.Fprintf(os.Stdout, "gb %s — %s\n", cmd.name, cmd.summary)
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := cmd.run(ctx, e)
	if err == nil {
		return 0
	}
	code := 1
	var ue usageError
	var xe exitError
	switch {
	case errors.As(err, &ue):
		code = 2
	case errors.As(err, &xe):
		code = xe.code
	case errors.Is(err, context.Canceled):
		code = 130
	}
	if e.json {
		out := map[string]any{"error": err.Error(), "exitCode": code}
		var ge *box.GatewayError
		if errors.As(err, &ge) {
			out["gateway"] = ge
		}
		b, _ := json.Marshal(out)
		fmt.Fprintln(os.Stdout, string(b))
	} else {
		fmt.Fprintln(os.Stderr, "gb:", err)
	}
	return code
}

// readMessage joins positional words, or reads stdin when the message is "-" or omitted with piped input.
func readMessage(words []string) (string, error) {
	if len(words) == 1 && words[0] == "-" || len(words) == 0 {
		if st, _ := os.Stdin.Stat(); len(words) == 1 || (st.Mode()&os.ModeCharDevice) == 0 {
			b, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
			if err != nil {
				return "", err
			}
			if s := strings.TrimRight(string(b), "\n"); s != "" {
				return s, nil
			}
		}
		return "", usage("message is empty")
	}
	return strings.Join(words, " "), nil
}

func ago(ms float64) string {
	if ms == 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(int64(ms)))
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func sortAgents(list []api.Agent) {
	sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
}
