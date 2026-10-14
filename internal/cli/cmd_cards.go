package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gb/internal/api"
)

// cardTargets resolves [bot] or, when omitted, every bot that reports waiting on the user (--all: every bot).
func cardTargets(ctx context.Context, e *env, ref string) (*api.API, []api.Agent, error) {
	if ref != "" {
		x, a, err := e.target(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		return x, []api.Agent{*a}, nil
	}
	x, err := e.API()
	if err != nil {
		return nil, nil, err
	}
	all, err := x.Agents(ctx, true)
	if err != nil {
		return nil, nil, err
	}
	var out []api.Agent
	for _, a := range all {
		if a.Awaiting() || e.a.bool("all") {
			out = append(out, a)
		}
	}
	return x, out, nil
}

func printCard(w io.Writer, c *api.PendingCard) {
	fmt.Fprintf(w, "%s  [%s]  entry=%s\n", c.AgentName, c.Kind, c.EntryID)
	switch c.Kind {
	case "approval":
		ap := c.Approval
		for _, kv := range [][2]string{{"command", ap.Command}, {"action", ap.Action}, {"summary", ap.Summary}, {"reason", ap.Reason}, {"target", ap.Target}, {"cwd", ap.WorkingDirectory}} {
			if kv[1] != "" {
				fmt.Fprintf(w, "    %-8s %s\n", kv[0], trunc(kv[1], 300))
			}
		}
		fmt.Fprintf(w, "    → gb approvals approve|deny %s %s %s   (--always, --command \"edited\")\n", short(c.AgentID), c.EntryID, c.RequestID)
	case "widget":
		fmt.Fprintf(w, "    %s\n", c.Widget.Prompt)
		for i, o := range c.Widget.Options {
			fmt.Fprintf(w, "    %d) %s", i+1, o.Label)
			if o.Description != "" {
				fmt.Fprintf(w, " — %s", trunc(o.Description, 80))
			}
			fmt.Fprintln(w)
		}
		mode := "one option"
		if c.Widget.MultiSelect {
			mode = "one or more options"
		}
		fmt.Fprintf(w, "    → gb answer %s %s <%s>", short(c.AgentID), c.EntryID, mode)
		if c.Widget.AllowCustom {
			fmt.Fprint(w, " [--custom TEXT]")
		}
		fmt.Fprintln(w, "  (or --dismiss)")
	case "form":
		fmt.Fprintf(w, "    %s — %s\n", c.Form.Title, trunc(c.Form.Instruction, 200))
		for _, f := range c.Form.Fields {
			req := ""
			if f.Required {
				req = " (required)"
			}
			fmt.Fprintf(w, "    • %s: %s [%s]%s\n", f.ID, f.Label, f.Type, req)
		}
		fmt.Fprintf(w, "    → gb form %s %s id=value ...   (or --dismiss)\n", short(c.AgentID), c.EntryID)
	case "secret":
		fmt.Fprintf(w, "    %s %s\n", c.Secret.Label, trunc(c.Secret.Description, 200))
		fmt.Fprintf(w, "    → gb secret %s %s   (reads the value from stdin; or env:VAR)\n", short(c.AgentID), c.EntryID)
	case "grant":
		fmt.Fprintf(w, "    use %s\n", c.Grant.ServerName)
		fmt.Fprintf(w, "    → gb grant %s %s allow|allow-bot|allow-team|skip\n", short(c.AgentID), c.EntryID)
	}
}

// cmdPending lists every card waiting for you: approvals, questions, forms, secret requests, app grants.
func cmdPending(ctx context.Context, e *env) error {
	ref := ""
	if len(e.a.pos) > 0 {
		ref = e.a.pos[0]
	}
	x, targets, err := cardTargets(ctx, e, ref)
	if err != nil {
		return err
	}
	res := []api.PendingCard{}
	for i := range targets {
		cs, err := x.Cards(ctx, &targets[i])
		if err != nil {
			return err
		}
		res = append(res, cs...)
	}
	e.emit(res, func(w io.Writer) {
		if len(res) == 0 {
			fmt.Fprintln(w, "Nothing is waiting for you.")
			return
		}
		for i := range res {
			printCard(w, &res[i])
		}
	})
	return nil
}

// respondCard is shared by answer/form/secret/grant/approvals.
func respondCard(ctx context.Context, e *env, kind string, r api.CardResponse) error {
	if len(e.a.pos) < 2 {
		return usage("gb %s <bot> <entryId> ...", kind)
	}
	x, ag, err := e.target(ctx, e.a.pos[0])
	if err != nil {
		return err
	}
	c, err := x.FindCard(ctx, ag, e.a.pos[1])
	if err != nil {
		return err
	}
	if c.Kind != kind {
		return fmt.Errorf("entry %s is a %s card, not %s (gb pending)", c.EntryID, c.Kind, kind)
	}
	if err := x.Respond(ctx, ag, c, r); err != nil {
		return err
	}
	e.emit(map[string]any{"agentId": ag.ID, "entryId": c.EntryID, "kind": c.Kind, "action": r.Action}, func(w io.Writer) {
		fmt.Fprintf(w, "%s: %s %s\n", ag.Name, c.Kind, r.Action)
	})
	return nil
}

func cmdAnswer(ctx context.Context, e *env) error {
	r := api.CardResponse{Action: "answer", Custom: e.a.get("custom", "")}
	if e.a.bool("dismiss") {
		r.Action = "dismiss"
	} else if len(e.a.pos) > 2 {
		r.Values = e.a.pos[2:]
	}
	return respondCard(ctx, e, "widget", r)
}

func cmdForm(ctx context.Context, e *env) error {
	r := api.CardResponse{Action: "submit", Fields: map[string]string{}}
	if e.a.bool("dismiss") {
		r.Action = "dismiss"
	}
	if len(e.a.pos) > 2 {
		for _, kv := range e.a.pos[2:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return usage("form fields are id=value (use env:VAR to read a value from the environment)")
			}
			if name, ok := strings.CutPrefix(v, "env:"); ok {
				v = os.Getenv(name)
			}
			r.Fields[k] = v
		}
	}
	return respondCard(ctx, e, "form", r)
}

func cmdSecret(ctx context.Context, e *env) error {
	v := ""
	if len(e.a.pos) > 2 {
		v = e.a.pos[2]
		if name, ok := strings.CutPrefix(v, "env:"); ok {
			v = os.Getenv(name)
		} else if v != "-" {
			fmt.Fprintln(os.Stderr, "note: a secret on the command line ends up in shell history; prefer stdin or env:VAR")
		}
	}
	if v == "" || v == "-" {
		if st, _ := os.Stdin.Stat(); st.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprint(os.Stderr, "Secret value (input is visible; end with Enter): ")
		}
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err != nil {
			return err
		}
		v = strings.TrimRight(string(b), "\r\n")
	}
	if v == "" {
		return errors.New("empty secret")
	}
	return respondCard(ctx, e, "secret", api.CardResponse{Action: "submit", Value: v})
}

func cmdGrant(ctx context.Context, e *env) error {
	if len(e.a.pos) < 3 {
		return usage("gb grant <bot> <entryId> allow|allow-bot|allow-team|skip")
	}
	return respondCard(ctx, e, "grant", api.CardResponse{Action: e.a.pos[2]})
}
