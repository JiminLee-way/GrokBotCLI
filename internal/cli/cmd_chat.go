package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gb/internal/api"
	"gb/internal/box"
)

func printEntry(w io.Writer, en *api.Entry, botName string) {
	body := strings.TrimRight(en.Body(), "\n")
	who := en.Speaker(botName)
	lines := strings.Split(body, "\n")
	fmt.Fprintf(w, "%s  %-12s %s\n", en.Time(), trunc(who, 12), lines[0])
	for _, l := range lines[1:] {
		fmt.Fprintf(w, "%s  %-12s %s\n", "     ", "", l)
	}
}

func cmdSend(ctx context.Context, e *env) error {
	if e.a.bool("wait") {
		return cmdAsk(ctx, e)
	}
	x, ag, msg, opts, err := prepareSend(ctx, e)
	if err != nil {
		return err
	}
	r, err := x.Send(ctx, ag, msg, opts)
	if r != nil {
		e.emit(r, func(w io.Writer) {
			fmt.Fprintf(w, "→ %s: %s", ag.Name, r.Delivery)
			if r.Reason != "" {
				fmt.Fprintf(w, " (%s)", r.Reason)
			}
			fmt.Fprintf(w, "  nonce=%s\n", r.ClientNonce)
		})
	}
	if err == nil && r.Delivery == "rejected" {
		return exitError{1, fmt.Errorf("prompt rejected: %s", r.Reason)}
	}
	return err
}

func prepareSend(ctx context.Context, e *env) (*api.API, *api.Agent, string, api.SendOpts, error) {
	var opts api.SendOpts
	if len(e.a.pos) < 1 {
		return nil, nil, "", opts, usage("gb send <bot|group> <message|->")
	}
	msg, err := readMessage(e.a.pos[1:])
	if err != nil {
		return nil, nil, "", opts, err
	}
	x, ag, err := e.target(ctx, e.a.pos[0])
	if err != nil {
		return nil, nil, "", opts, err
	}
	opts.ReplyToID = e.a.get("reply-to", "")
	opts.ClientNonce = e.a.get("nonce", "")
	for _, f := range e.a.list("file") {
		path, err := upload(ctx, x, ag.ID, f)
		if err != nil {
			return nil, nil, "", opts, err
		}
		opts.AttachmentPaths = append(opts.AttachmentPaths, path)
		opts.AttachmentNames = append(opts.AttachmentNames, filepath.Base(f))
	}
	return x, ag, msg, opts, nil
}

func upload(ctx context.Context, x *api.API, agentID, file string) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return x.Upload(ctx, agentID, filepath.Base(file), b)
}

// tracker diffs transcript tails and reports finalized entries once.
type tracker struct {
	x     *api.API
	seen  map[string]string // agentId/entryId -> last printed body
	init  map[string]bool
	since float64 // entries older than this (ms) are history, not news, on an agent's first poll
	mu    sync.Mutex
	limit int
}

func newTracker(x *api.API) *tracker {
	return &tracker{x: x, seen: map[string]string{}, init: map[string]bool{}, limit: 40, since: float64(time.Now().UnixMilli())}
}

func (t *tracker) baseline(ctx context.Context, id string) error {
	p, err := t.x.Tail(ctx, id, t.limit, 0)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, en := range p.Entries {
		t.seen[id+"/"+en.ID] = en.Body()
	}
	t.init[id] = true
	return nil
}

// poll returns new finalized entries and whether any entry is still being written.
func (t *tracker) poll(ctx context.Context, id string) ([]api.Entry, bool, error) {
	p, err := t.x.Tail(ctx, id, t.limit, 0)
	if err != nil {
		return nil, false, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []api.Entry
	pending := false
	first := !t.init[id]
	t.init[id] = true
	for _, en := range p.Entries {
		if first && en.TimestampMs > 0 && en.TimestampMs < t.since {
			t.seen[id+"/"+en.ID] = en.Body()
			continue
		}
		if en.Pending() {
			pending = true
			continue
		}
		key, body := id+"/"+en.ID, en.Body()
		if prev, ok := t.seen[key]; ok && prev == body {
			continue
		}
		t.seen[key] = body
		out = append(out, en)
	}
	return out, pending, nil
}

// activity returns a channel that fires when the event stream mentions agentID (or any agent when "").
func activity(ctx context.Context, x *api.API, busy map[string]*bool, mu *sync.Mutex) (<-chan string, <-chan struct{}) {
	ch := make(chan string, 64)
	connected := make(chan struct{}, 1)
	go func() {
		_ = x.Events(ctx, func() {
			select {
			case connected <- struct{}{}:
			default:
			}
			select {
			case ch <- "":
			default:
			}
		}, func(ev box.Event) {
			var p struct {
				AgentID       string     `json:"agentId"`
				ActiveAgentID string     `json:"activeAgentId"`
				Agent         *api.Agent `json:"agent"`
				Live          *struct {
					IsRunning     bool  `json:"isRunning"`
					IsRunningTurn *bool `json:"isRunningTurn"`
				} `json:"live"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			id := p.AgentID
			switch ev.Channel {
			case "agent-activity":
				if p.Live != nil && mu != nil {
					b := p.Live.IsRunning || (p.Live.IsRunningTurn != nil && *p.Live.IsRunningTurn)
					mu.Lock()
					busy[id] = &b
					mu.Unlock()
				}
			case "agent-upserted":
				if p.Agent != nil {
					id = p.Agent.ID
					if mu != nil {
						b := p.Agent.Busy()
						mu.Lock()
						busy[id] = &b
						mu.Unlock()
					}
				}
			case "transcript":
				if id == "" {
					id = p.ActiveAgentID
				}
			default:
				return
			}
			if os.Getenv("GB_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "event %s %s\n", ev.Channel, id)
			}
			select {
			case ch <- id:
			default:
			}
		})
	}()
	return ch, connected
}

func cmdAsk(ctx context.Context, e *env) error {
	x, ag, msg, opts, err := prepareSend(ctx, e)
	if err != nil {
		return err
	}
	timeout, err := time.ParseDuration(e.a.get("timeout", "10m"))
	if err != nil {
		return usage("--timeout: %v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tr := newTracker(x)
	if err := tr.baseline(ctx, ag.ID); err != nil {
		return err
	}
	var mu sync.Mutex
	busy := map[string]*bool{}
	wake, _ := activity(ctx, x, busy, &mu)

	r, err := x.Send(ctx, ag, msg, opts)
	if err != nil {
		if r != nil && e.json {
			e.emit(r, nil)
		}
		return err
	}
	if r.Delivery == "rejected" {
		return exitError{1, fmt.Errorf("prompt rejected: %s", r.Reason)}
	}
	if !e.json && !e.a.bool("quiet") {
		fmt.Fprintf(os.Stderr, "→ %s (%s), waiting for reply...\n", ag.Name, r.Delivery)
	}

	showTools := e.a.bool("show-tools")
	var replies []api.Entry
	gotBot, quietTicks := false, 0
	start := time.Now()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				e.emit(map[string]any{"send": r, "entries": replies, "done": false}, func(io.Writer) {})
				return exitError{124, fmt.Errorf("timed out after %s waiting for %s (it may still be working: gb watch %s)", timeout, ag.Name, ag.Name)}
			}
			return ctx.Err()
		case <-wake:
		case <-tick.C:
		}
		news, pending, err := tr.poll(ctx, ag.ID)
		if err != nil {
			var ge *box.GatewayError
			if errors.As(err, &ge) && ge.Status == 0 {
				continue // transient network error; keep waiting
			}
			return err
		}
		for _, en := range news {
			if !en.FromBot() {
				continue
			}
			if en.Kind == "tool-call" && !showTools {
				continue
			}
			gotBot = gotBot || en.Kind != "tool-call"
			replies = append(replies, en)
			if !e.json {
				printEntry(e.out, &en, ag.Name)
			}
			if ap := en.PendingApproval(); ap != nil && !e.json {
				fmt.Fprintf(os.Stderr, "⚠ %s is waiting for approval: gb approvals approve %s %s %s\n", ag.Name, short(ag.ID), en.ID, ap.RequestID)
			}
		}
		mu.Lock()
		b := busy[ag.ID]
		mu.Unlock()
		running := pending
		if b != nil {
			running = running || *b
		} else if cur, err := x.Agents(ctx, true); err == nil { // no live activity info yet: ask the roster
			for i := range cur {
				if cur[i].ID == ag.ID {
					running = running || cur[i].Busy()
					if cur[i].Awaiting() && gotBot {
						running = false
					}
				}
			}
		}
		if !running && (gotBot || time.Since(start) > 90*time.Second) {
			quietTicks++
		} else {
			quietTicks = 0
		}
		if quietTicks >= 2 {
			break
		}
	}
	texts := []string{}
	for _, en := range replies {
		if en.Kind != "tool-call" {
			texts = append(texts, en.Body())
		}
	}
	if e.json {
		e.emit(map[string]any{"send": r, "entries": replies, "reply": strings.Join(texts, "\n\n"), "done": true}, nil)
	} else if !gotBot {
		fmt.Fprintf(os.Stderr, "(%s finished without a reply)\n", ag.Name)
	}
	return nil
}

func cmdLog(ctx context.Context, e *env) error {
	if len(e.a.pos) < 1 {
		return usage("gb log <bot|group> [-n 30] [--before SEQ] [--after ENTRY_ID]")
	}
	x, ag, err := e.target(ctx, e.a.pos[0])
	if err != nil {
		return err
	}
	limit := e.a.int("limit", 30)
	before, _ := strconv.ParseFloat(e.a.get("before", "0"), 64)
	p, err := x.Tail(ctx, ag.ID, limit, before)
	if err != nil {
		return err
	}
	entries := p.Entries
	gapReset := false
	if after, ok := e.a.str("after"); ok {
		idx := -1
		for i, en := range entries {
			if en.ID == after {
				idx = i
			}
		}
		if idx >= 0 {
			entries = entries[idx+1:]
		} else {
			gapReset = true
		}
	}
	cursor := ""
	if len(entries) > 0 {
		cursor = entries[len(entries)-1].ID
	}
	out := map[string]any{"target": map[string]string{"id": ag.ID, "name": ag.Name}, "entries": entries, "entryCount": len(entries), "cursor": cursor, "gapReset": gapReset}
	if p.NextBeforeSeq != nil {
		out["nextBeforeSeq"] = *p.NextBeforeSeq
	}
	e.emit(out, func(w io.Writer) {
		for i := range entries {
			printEntry(w, &entries[i], ag.Name)
		}
		if p.NextBeforeSeq != nil && e.a.optStr("after") == nil {
			fmt.Fprintf(w, "(older: gb log %s --before %.0f)\n", e.a.pos[0], *p.NextBeforeSeq)
		}
	})
	return nil
}

func cmdThread(ctx context.Context, e *env) error {
	if len(e.a.pos) < 2 {
		return usage("gb thread <bot|group> <rootEntryId>")
	}
	x, ag, err := e.target(ctx, e.a.pos[0])
	if err != nil {
		return err
	}
	p, err := x.Thread(ctx, ag.ID, e.a.pos[1])
	if err != nil {
		return err
	}
	e.emit(p, func(w io.Writer) {
		for i := range p.Entries {
			printEntry(w, &p.Entries[i], ag.Name)
		}
	})
	return nil
}

func cmdWatch(ctx context.Context, e *env) error {
	x, err := e.API()
	if err != nil {
		return err
	}
	watch := map[string]bool{}
	for n, ref := range e.a.pos {
		y, a, err := e.target(ctx, ref)
		if err != nil {
			return err
		}
		if n == 0 {
			x = y
		} else if y != x {
			return usage("watch one account at a time (run one `gb watch` per account)")
		}
		watch[a.ID] = true
	}
	all, err := x.Agents(ctx, true)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, a := range all {
		names[a.ID] = a.Name
	}
	tr := newTracker(x)
	known := map[string]bool{}
	for id := range watch {
		known[id] = true
		_, _, _ = tr.poll(ctx, id) // baseline: everything before now is history
	}
	approvalsOnly := e.a.bool("approvals")
	wake, _ := activity(ctx, x, nil, nil)
	if !e.json {
		target := "all bots"
		if len(watch) > 0 {
			target = fmt.Sprintf("%d bot(s)", len(watch))
		}
		fmt.Fprintf(os.Stderr, "watching %s (Ctrl-C to stop)\n", target)
	}
	enc := json.NewEncoder(e.out)
	enc.SetEscapeHTML(false)
	safety := time.NewTicker(30 * time.Second)
	defer safety.Stop()
	for {
		var ids []string
		select {
		case <-ctx.Done():
			return nil
		case id := <-wake:
			if id == "" {
				continue
			}
			ids = []string{id}
		case <-safety.C:
			for id := range known {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			if len(watch) > 0 && !watch[id] {
				continue
			}
			known[id] = true
			news, _, err := tr.poll(ctx, id)
			if err != nil {
				continue
			}
			if _, ok := names[id]; !ok {
				if a, err := x.Resolve(ctx, id); err == nil {
					names[id] = a.Name
				}
			}
			for _, en := range news {
				ap := en.PendingApproval()
				if approvalsOnly && ap == nil {
					continue
				}
				if e.json {
					_ = enc.Encode(map[string]any{"agentId": id, "agentName": names[id], "entry": en, "approval": ap})
					continue
				}
				fmt.Fprintf(e.out, "[%s] ", names[id])
				printEntry(e.out, &en, names[id])
			}
		}
	}
}

func cmdSearch(ctx context.Context, e *env) error {
	if len(e.a.pos) == 0 {
		return usage("gb search <query>")
	}
	x, err := e.API()
	if err != nil {
		return err
	}
	var res []struct {
		AgentID     string  `json:"agentId"`
		EntryID     string  `json:"entryId"`
		Role        string  `json:"role"`
		Snippet     string  `json:"snippet"`
		TimestampMs float64 `json:"timestampMs"`
	}
	if err := x.Call(ctx, "searchAgents", map[string]any{"query": strings.Join(e.a.pos, " "), "limit": e.a.int("limit", 20)}, &res); err != nil {
		return err
	}
	all, _ := x.Agents(ctx, false)
	names := map[string]string{}
	for _, a := range all {
		names[a.ID] = a.Name
	}
	e.emit(res, func(w io.Writer) {
		rows := [][]string{}
		for _, r := range res {
			rows = append(rows, []string{ago(r.TimestampMs), firstOr(names[r.AgentID], short(r.AgentID)), r.Role, r.EntryID, trunc(r.Snippet, 70)})
		}
		table(w, []string{"WHEN", "BOT", "ROLE", "ENTRY", "SNIPPET"}, rows)
	})
	return nil
}

func simpleAgentCall(method string, label string) func(context.Context, *env) error {
	return func(ctx context.Context, e *env) error {
		if len(e.a.pos) < 1 {
			return usage("gb %s <bot>", label)
		}
		x, ag, err := e.target(ctx, e.a.pos[0])
		if err != nil {
			return err
		}
		var r json.RawMessage
		if err := x.Call(ctx, method, map[string]any{"id": ag.ID}, &r); err != nil {
			var ge *box.GatewayError
			if errors.As(err, &ge) && ge.Status == 409 && method != "interruptAgentRun" {
				// Like the app, reset/undo are only offered after a turn failed or was blocked.
				return fmt.Errorf("%s: %s (only available right after a failed or blocked turn)", ag.Name, ge.Message)
			}
			return err
		}
		e.emit(map[string]any{"target": ag.ID, "result": r}, func(w io.Writer) { fmt.Fprintf(w, "%s: %s done\n", ag.Name, label) })
		return nil
	}
}

var cmdStop = simpleAgentCall("interruptAgentRun", "stop")
var cmdReset = simpleAgentCall("startFreshConversation", "reset")
var cmdUndo = simpleAgentCall("rollBackConversation", "undo")

func cmdEvents(ctx context.Context, e *env) error {
	x, err := e.API()
	if err != nil {
		return err
	}
	only := map[string]bool{}
	for _, c := range e.a.list("channel") {
		only[c] = true
	}
	enc := json.NewEncoder(e.out)
	enc.SetEscapeHTML(false)
	return x.Events(ctx, func() { fmt.Fprintln(os.Stderr, "# connected") }, func(ev box.Event) {
		if len(only) == 0 || only[ev.Channel] {
			_ = enc.Encode(ev)
		}
	})
}
