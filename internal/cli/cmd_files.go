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
	"time"

	"gb/internal/api"
	"gb/internal/box"
)

// ---------- files a bot produced ----------

func cmdFiles(ctx context.Context, e *env) error {
	sub := "list"
	if len(e.a.pos) > 0 {
		sub, e.a.pos = e.a.pos[0], e.a.pos[1:]
	}
	switch sub {
	case "list", "ls":
		if len(e.a.pos) == 0 {
			return usage("gb files list <bot> [-n 50]")
		}
		x, ag, err := e.target(ctx, e.a.pos[0])
		if err != nil {
			return err
		}
		p, err := x.Tail(ctx, ag.ID, e.a.int("limit", 50), 0)
		if err != nil {
			return err
		}
		type row struct {
			EntryID string `json:"entryId"`
			When    string `json:"when"`
			FromBot bool   `json:"fromBot"`
			api.File
		}
		var out []row
		for i := range p.Entries {
			en := &p.Entries[i]
			for _, f := range en.Files() {
				out = append(out, row{en.ID, en.Time(), en.FromBot(), f})
			}
		}
		e.emit(out, func(w io.Writer) {
			if len(out) == 0 {
				fmt.Fprintln(w, "No files in the recent conversation.")
				return
			}
			t := [][]string{}
			for _, r := range out {
				who := "you"
				if r.FromBot {
					who = ag.Name
				}
				t = append(t, []string{r.When, who, r.Kind, r.Name, firstOr(r.Path, r.URL)})
			}
			table(w, []string{"WHEN", "FROM", "KIND", "NAME", "PATH/URL"}, t)
			fmt.Fprintf(w, "(download: gb files get %s <path> [-o FILE])\n", e.a.pos[0])
		})
		return nil

	case "get", "download":
		if len(e.a.pos) < 2 {
			return usage("gb files get <bot> <path|url> [-o FILE|-]")
		}
		x, ag, err := e.target(ctx, e.a.pos[0])
		if err != nil {
			return err
		}
		f := api.File{URL: e.a.pos[1]}
		if p, ok := strings.CutPrefix(f.URL, "file://"); ok {
			f.Path = p
		} else if strings.HasPrefix(f.URL, "/") {
			f.Path = f.URL
		}
		f.Name = filepath.Base(firstOr(f.Path, f.URL))
		dest := e.a.get("output", e.a.get("o", f.Name))
		var w io.Writer = os.Stdout
		var file *os.File
		if dest != "-" {
			if file, err = os.Create(dest); err != nil {
				return err
			}
			defer file.Close()
			w = file
		}
		info, err := x.ReadFile(ctx, ag.ID, f, w, int64(e.a.int("max-mb", 0))<<20)
		if err != nil {
			if file != nil {
				os.Remove(dest)
			}
			return err
		}
		if dest != "-" {
			fmt.Fprintf(os.Stderr, "Saved %s (%s, %s)\n", dest, api.HumanSize(info.Size), firstOr(info.Mime, "?"))
		}
		return nil

	case "search":
		if len(e.a.pos) == 0 {
			return usage("gb files search <query>")
		}
		x, err := e.API()
		if err != nil {
			return err
		}
		var res []struct {
			AgentID  string  `json:"agentId"`
			EntryID  string  `json:"entryId"`
			FileName string  `json:"fileName"`
			Kind     string  `json:"kind"`
			Mime     string  `json:"mime"`
			TS       float64 `json:"timestampMs"`
		}
		if err := x.Call(ctx, "searchMedia", map[string]any{"query": strings.Join(e.a.pos, " "), "limit": e.a.int("limit", 20)}, &res); err != nil {
			return err
		}
		all, _ := x.Agents(ctx, false)
		names := map[string]string{}
		for _, a := range all {
			names[a.ID] = a.Name
		}
		e.emit(res, func(w io.Writer) {
			t := [][]string{}
			for _, r := range res {
				t = append(t, []string{ago(r.TS), firstOr(names[r.AgentID], short(r.AgentID)), r.Kind, r.FileName, r.EntryID})
			}
			table(w, []string{"WHEN", "BOT", "KIND", "FILE", "ENTRY"}, t)
		})
		return nil
	}
	return usage("gb files list <bot> | get <bot> <path> [-o FILE] | search <query>")
}

// ---------- automations (routines) ----------

type automation struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Prompt      string          `json:"prompt"`
	IsEnabled   bool            `json:"isEnabled"`
	Schedule    string          `json:"schedule"`
	Trigger     json.RawMessage `json:"trigger"`
	TriggerDesc string          `json:"triggerDescription"`
	NextRunAt   *float64        `json:"nextRunAt"`
	LastRunAt   *float64        `json:"lastRunAt"`
}

func when(ms *float64) string {
	if ms == nil || *ms == 0 {
		return "-"
	}
	return time.UnixMilli(int64(*ms)).Format("01-02 15:04")
}

func cmdAuto(ctx context.Context, e *env) error {
	sub := "list"
	if len(e.a.pos) > 0 {
		sub, e.a.pos = e.a.pos[0], e.a.pos[1:]
	}
	if sub == "list" && len(e.a.pos) == 0 {
		x, err := e.API()
		if err != nil {
			return err
		}
		var all []struct {
			AgentID    string     `json:"agentId"`
			Automation automation `json:"automation"`
		}
		if err := x.Call(ctx, "listAllAutomations", nil, &all); err != nil {
			return err
		}
		agents, _ := x.Agents(ctx, false)
		names := map[string]string{}
		for _, a := range agents {
			names[a.ID] = a.Name
		}
		e.emit(all, func(w io.Writer) {
			t := [][]string{}
			for _, r := range all {
				a := r.Automation
				t = append(t, []string{firstOr(names[r.AgentID], short(r.AgentID)), short(a.ID), a.Name, onoff(a.IsEnabled), firstOr(a.TriggerDesc, a.Schedule), when(a.NextRunAt)})
			}
			table(w, []string{"BOT", "ID", "NAME", "STATE", "TRIGGER", "NEXT RUN"}, t)
		})
		return nil
	}
	if len(e.a.pos) == 0 {
		return usage("gb auto list [bot] | add <bot> --name N --prompt P (--cron EXPR | --webhook) | update|on|off|run|rm|webhook <bot> <automation>")
	}
	x, ag, err := e.target(ctx, e.a.pos[0])
	if err != nil {
		return err
	}
	list := func() ([]automation, error) {
		var l []automation
		return l, x.Call(ctx, "getAgentAutomations", map[string]any{"id": ag.ID}, &l)
	}
	find := func() (*automation, error) {
		if len(e.a.pos) < 2 {
			return nil, usage("gb auto %s <bot> <automation id|name>", sub)
		}
		l, err := list()
		if err != nil {
			return nil, err
		}
		ref := e.a.pos[1]
		for i := range l {
			if l[i].ID == ref || strings.HasPrefix(l[i].ID, ref) || strings.EqualFold(l[i].Name, ref) {
				return &l[i], nil
			}
		}
		return nil, fmt.Errorf("%s has no automation %q", ag.Name, ref)
	}
	show := func(l []automation) {
		e.emit(l, func(w io.Writer) {
			t := [][]string{}
			for _, a := range l {
				t = append(t, []string{short(a.ID), a.Name, onoff(a.IsEnabled), firstOr(a.TriggerDesc, a.Schedule), when(a.NextRunAt), when(a.LastRunAt)})
			}
			table(w, []string{"ID", "NAME", "STATE", "TRIGGER", "NEXT RUN", "LAST RUN"}, t)
		})
	}
	trigger := func(cur json.RawMessage) (any, error) {
		cron, hasCron := e.a.str("cron")
		switch {
		case hasCron && e.a.bool("webhook"):
			return nil, usage("use --cron or --webhook, not both")
		case hasCron:
			t := map[string]any{"type": "cron", "schedule": cron}
			if e.a.bool("exact") {
				t["timing"] = "exact"
			}
			return t, nil
		case e.a.bool("webhook"):
			return map[string]any{"type": "webhook"}, nil
		case cur != nil:
			return cur, nil
		}
		return nil, usage(`a trigger is required: --cron "0 9 * * *" (minute hour day month weekday) or --webhook`)
	}
	var out []automation
	switch sub {
	case "list", "ls":
		l, err := list()
		if err != nil {
			return err
		}
		show(l)
		return nil
	case "add", "create":
		name, prompt := e.a.get("name", ""), e.a.get("prompt", "")
		if name == "" || prompt == "" {
			return usage("gb auto add <bot> --name NAME --prompt TEXT (--cron EXPR | --webhook)")
		}
		t, err := trigger(nil)
		if err != nil {
			return err
		}
		spec := map[string]any{"name": name, "prompt": prompt, "trigger": t, "isEnabled": !e.a.bool("off")}
		if err := x.Call(ctx, "createAgentAutomation", map[string]any{"id": ag.ID, "spec": spec}, &out); err != nil {
			return err
		}
	case "update", "edit":
		a, err := find()
		if err != nil {
			return err
		}
		t, err := trigger(a.Trigger)
		if err != nil {
			return err
		}
		spec := map[string]any{"name": e.a.get("name", a.Name), "prompt": e.a.get("prompt", a.Prompt), "trigger": t, "isEnabled": a.IsEnabled}
		if err := x.Call(ctx, "updateAgentAutomation", map[string]any{"id": ag.ID, "automationId": a.ID, "spec": spec}, &out); err != nil {
			return err
		}
	case "on", "off", "enable", "disable":
		a, err := find()
		if err != nil {
			return err
		}
		on := sub == "on" || sub == "enable"
		if err := x.Call(ctx, "setAgentAutomationEnabled", map[string]any{"id": ag.ID, "automationId": a.ID, "isEnabled": on}, &out); err != nil {
			return err
		}
	case "run":
		a, err := find()
		if err != nil {
			return err
		}
		if e.a.bool("send") { // run the routine's prompt as a normal message
			r, err := x.Send(ctx, ag, a.Prompt, api.SendOpts{})
			if err != nil {
				return err
			}
			fmt.Fprintf(e.out, "Sent %s's prompt to %s (%s)\n", a.Name, ag.Name, r.Delivery)
			return nil
		}
		if err := x.Call(ctx, "runAgentAutomationNow", map[string]any{"id": ag.ID, "automationId": a.ID}, nil); err != nil {
			var ge *box.GatewayError
			if errors.As(err, &ge) && ge.FailureCode == "automation-run-now/refused" {
				return fmt.Errorf("the server refused to run %q now (it does this for some server-hosted bots); run its prompt directly with: gb auto run %s %s --send", a.Name, e.a.pos[0], e.a.pos[1])
			}
			return err
		}
		fmt.Fprintf(e.out, "Started %s now (follow with: gb watch %s)\n", a.Name, e.a.pos[0])
		return nil
	case "rm", "delete":
		a, err := find()
		if err != nil {
			return err
		}
		if err := x.Call(ctx, "deleteAgentAutomation", map[string]any{"id": ag.ID, "automationId": a.ID}, &out); err != nil {
			return err
		}
	case "webhook":
		a, err := find()
		if err != nil {
			return err
		}
		var cred struct {
			URL string  `json:"url"`
			Key *string `json:"key"`
		}
		if err := x.Call(ctx, "getAutomationWebhookCredential", map[string]any{"id": ag.ID, "automationId": a.ID}, &cred); err != nil {
			return err
		}
		e.emit(cred, func(w io.Writer) {
			fmt.Fprintf(w, "POST %s\n", cred.URL)
			if cred.Key != nil {
				fmt.Fprintf(w, "key: %s\n", *cred.Key)
			}
		})
		return nil
	default:
		return usage("gb auto list|add|update|on|off|run|rm|webhook")
	}
	show(out)
	return nil
}

func onoff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// ---------- alerts (the app's error trays: usage limits, paused routines, failed syncs, ...) ----------

func cmdAlerts(ctx context.Context, e *env) error {
	x, err := e.API()
	if err != nil {
		return err
	}
	if len(e.a.pos) > 0 && e.a.pos[0] == "dismiss" {
		if len(e.a.pos) < 2 {
			return usage("gb alerts dismiss <id>")
		}
		return x.Call(ctx, "dismissTray", map[string]any{"id": e.a.pos[1]}, nil)
	}
	if e.a.bool("clear") {
		return x.Call(ctx, "clearTrays", nil, nil)
	}
	var trays []struct {
		ID        string  `json:"id"`
		AgentID   string  `json:"agentId"`
		ErrorKind string  `json:"errorKind"`
		Title     string  `json:"title"`
		Detail    string  `json:"detail"`
		Count     float64 `json:"count"`
		CreatedAt float64 `json:"createdAt"`
	}
	if err := x.Call(ctx, "getTrays", nil, &trays); err != nil {
		return err
	}
	e.emit(trays, func(w io.Writer) {
		if len(trays) == 0 {
			fmt.Fprintln(w, "No alerts.")
			return
		}
		t := [][]string{}
		for _, a := range trays {
			t = append(t, []string{ago(a.CreatedAt), a.ID, a.ErrorKind, strconv.Itoa(int(a.Count)), trunc(firstOr(a.Title, a.Detail), 70)})
		}
		table(w, []string{"WHEN", "ID", "KIND", "COUNT", "DETAIL"}, t)
	})
	if len(trays) > 0 && e.a.bool("fail") {
		return exitError{1, errors.New("alerts present")}
	}
	return nil
}
