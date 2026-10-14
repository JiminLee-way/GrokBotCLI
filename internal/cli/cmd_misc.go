package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gb/internal/api"
	"gb/internal/box"
)

// ---------- skills (one library per account; the RPCs take any bot id) ----------

type skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	SourceRef   string `json:"sourceRef,omitempty"`
	PluginID    string `json:"pluginId,omitempty"`
}

func anyBot(ctx context.Context, x *api.API) (string, error) {
	all, err := x.Agents(ctx, false)
	if err != nil {
		return "", err
	}
	for _, a := range all {
		if !a.IsGroup {
			return a.ID, nil
		}
	}
	return "", errors.New("skills need at least one bot; run `gb bots create <name>` first")
}

func cmdSkills(ctx context.Context, e *env) error {
	sub := "list"
	if len(e.a.pos) > 0 {
		sub, e.a.pos = e.a.pos[0], e.a.pos[1:]
	}
	x, err := e.API()
	if err != nil {
		return err
	}
	id, err := anyBot(ctx, x)
	if err != nil {
		return err
	}
	list := func() ([]skill, error) {
		var s []skill
		return s, x.Call(ctx, "getAgentWorkflows", map[string]any{"id": id}, &s)
	}
	switch sub {
	case "list", "ls":
		s, err := list()
		if err != nil {
			return err
		}
		e.emit(s, func(w io.Writer) {
			rows := [][]string{}
			for _, k := range s {
				rows = append(rows, []string{short(k.ID), k.Name, k.Source, trunc(k.Description, 60)})
			}
			table(w, []string{"ID", "NAME", "SOURCE", "DESCRIPTION"}, rows)
		})
		return nil
	case "add", "import":
		if len(e.a.pos) == 0 {
			return usage("gb skills add <SKILL.md | dir | https://url>")
		}
		src := e.a.pos[0]
		args := map[string]any{"id": id}
		method := "importAgentWorkflowText"
		if strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://") {
			method, args["url"] = "importAgentWorkflowUrl", src
		} else {
			if st, err := os.Stat(src); err == nil && st.IsDir() {
				src = filepath.Join(src, "SKILL.md")
			}
			b, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if len(b) > 100000 {
				return fmt.Errorf("skill is %d bytes; Grok Bot truncates bodies over 100000", len(b))
			}
			args["markdown"] = string(b)
		}
		if n, ok := e.a.str("name"); ok {
			args["name"] = n
		}
		var r struct {
			Result struct {
				Imported []skill          `json:"imported"`
				Skipped  []map[string]any `json:"skipped"`
			} `json:"result"`
		}
		if err := x.Call(ctx, method, args, &r); err != nil {
			return err
		}
		if len(r.Result.Imported) == 0 {
			reason := "rejected"
			if len(r.Result.Skipped) > 0 {
				reason = fmt.Sprint(r.Result.Skipped[0]["reason"])
			}
			return fmt.Errorf("skill not imported (%s): it needs a name, a non-empty body and library room", reason)
		}
		e.emit(r.Result.Imported[0], func(w io.Writer) {
			fmt.Fprintf(w, "Added skill %s (%s)\n", r.Result.Imported[0].Name, r.Result.Imported[0].ID)
		})
		return nil
	case "remove", "rm", "delete":
		if len(e.a.pos) == 0 {
			return usage("gb skills remove <id|name>")
		}
		s, err := list()
		if err != nil {
			return err
		}
		needle := strings.ToLower(e.a.pos[0])
		var hit []skill
		for _, k := range s {
			if strings.ToLower(k.ID) == needle || strings.ToLower(k.Name) == needle || strings.HasPrefix(strings.ToLower(k.ID), needle) {
				hit = append(hit, k)
			}
		}
		if len(hit) != 1 {
			return fmt.Errorf("%d skills match %q; use the id", len(hit), e.a.pos[0])
		}
		if hit[0].Source != "workflow" {
			return fmt.Errorf("%q comes from %s, not your library; manage it there", hit[0].Name, hit[0].Source)
		}
		if err := x.Call(ctx, "deleteAgentWorkflow", map[string]any{"id": id, "workflowId": hit[0].ID}, nil); err != nil {
			return err
		}
		e.emit(hit[0], func(w io.Writer) { fmt.Fprintf(w, "Removed skill %s\n", hit[0].Name) })
		return nil
	}
	return usage("gb skills list|add|remove")
}

// ---------- approvals ----------

// cmdApprovals: approvals list [bot] | approve|deny <bot> <entryId> [requestId] [--always] [--never] [--command "edited"]
func cmdApprovals(ctx context.Context, e *env) error {
	sub := "list"
	if len(e.a.pos) > 0 {
		sub, e.a.pos = e.a.pos[0], e.a.pos[1:]
	}
	switch sub {
	case "list", "ls":
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
			for _, c := range cs {
				if c.Kind == "approval" {
					res = append(res, c)
				}
			}
		}
		e.emit(res, func(w io.Writer) {
			if len(res) == 0 {
				fmt.Fprintln(w, "No pending approvals.")
			}
			for i := range res {
				printCard(w, &res[i])
			}
		})
		return nil
	case "approve", "deny":
		action := sub
		if sub == "approve" && e.a.bool("always") {
			action = "always"
		}
		if sub == "deny" && e.a.bool("never") {
			action = "never"
		}
		return respondCard(ctx, e, "approval", api.CardResponse{Action: action, Command: e.a.get("command", "")})
	}
	return usage("gb approvals list [bot] | approve|deny <bot> <entryId> [--always|--never] [--command TEXT]")
}

// ---------- raw call ----------

func cmdCall(ctx context.Context, e *env) error {
	if len(e.a.pos) < 1 {
		return usage("gb call <method> ['{json}' | -]")
	}
	method := e.a.pos[0]
	var args any = map[string]any{}
	if len(e.a.pos) > 1 {
		raw := []byte(e.a.pos[1])
		if e.a.pos[1] == "-" {
			b, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<20))
			if err != nil {
				return err
			}
			raw = b
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return usage("args must be JSON: %v", err)
		}
	}
	x, err := e.API()
	if err != nil {
		return err
	}
	var out json.RawMessage
	if err := x.Call(ctx, method, args, &out); err != nil {
		return err
	}
	if len(out) == 0 {
		out = json.RawMessage("null")
	}
	var v any
	_ = json.Unmarshal(out, &v)
	enc := json.NewEncoder(e.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// cmdRPC calls a Cursor backend Connect RPC: gb rpc <Service>/<Method> ['{json}' | -]
// Service may omit the "aiserver.v1." prefix (e.g. GrokBotService/GetGrokBotRuntimeCapabilities).
func cmdRPC(ctx context.Context, e *env) error {
	if len(e.a.pos) < 1 || !strings.Contains(e.a.pos[0], "/") {
		return usage("gb rpc <Service>/<Method> ['{json}' | -]")
	}
	rpc := e.a.pos[0]
	if !strings.HasPrefix(rpc, "aiserver.") {
		rpc = "aiserver.v1." + rpc
	}
	var args any = map[string]any{}
	if len(e.a.pos) > 1 {
		raw := []byte(e.a.pos[1])
		if e.a.pos[1] == "-" {
			b, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<20))
			if err != nil {
				return err
			}
			raw = b
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return usage("args must be JSON: %v", err)
		}
	}
	x, err := e.API()
	if err != nil {
		return err
	}
	var out any
	if err := box.Backend(ctx, x.P, rpc, args, &out); err != nil {
		return err
	}
	enc := json.NewEncoder(e.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}
