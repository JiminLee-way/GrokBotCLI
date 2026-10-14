package api

// Interactive cards a bot posts in its transcript and waits on:
//   approval — auto-review-approval / local-tool-permission (run a command, use a tool)
//   widget   — multiple-choice question (single or multi-select, optional custom answer)
//   form     — user-form (fields to fill, e.g. a login or checkout form)
//   secret   — secret-request (an API key or token the bot needs)
//   grant    — connector-grant (let the bot use a connected app)

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Widget struct {
	Prompt      string         `json:"prompt"`
	HelpText    string         `json:"helpText,omitempty"`
	Options     []WidgetOption `json:"options"`
	MultiSelect bool           `json:"multiSelect,omitempty"`
	AllowCustom bool           `json:"allowCustom,omitempty"`
}

type WidgetOption struct {
	Label       string `json:"label"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
	Style       string `json:"style,omitempty"`
}

// Answer is what the app sends for this option: value, else label.
func (o WidgetOption) Answer() string {
	if o.Value != "" {
		return o.Value
	}
	return o.Label
}

type FormRequest struct {
	Title        string      `json:"title"`
	Instruction  string      `json:"instruction"`
	Reason       string      `json:"reason"`
	Domain       string      `json:"domain,omitempty"`
	SecureValues bool        `json:"secureValues,omitempty"`
	Fields       []FormField `json:"fields"`
}

type FormField struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Options     []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"options,omitempty"`
}

type SecretRequest struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Target      struct {
		Kind     string `json:"kind"`
		Name     string `json:"name,omitempty"`
		Platform string `json:"platform,omitempty"`
		Field    string `json:"field,omitempty"`
		Key      string `json:"key,omitempty"`
	} `json:"target"`
}

// PendingCard is any card still waiting for an answer.
type PendingCard struct {
	AgentID   string         `json:"agentId"`
	AgentName string         `json:"agentName,omitempty"`
	EntryID   string         `json:"entryId"`
	Kind      string         `json:"kind"` // approval | widget | form | secret | grant
	Type      string         `json:"type"` // the raw send-message type
	RequestID string         `json:"requestId,omitempty"`
	Approval  *Card          `json:"approval,omitempty"`
	Widget    *Widget        `json:"widget,omitempty"`
	Form      *FormRequest   `json:"form,omitempty"`
	Secret    *SecretRequest `json:"secret,omitempty"`
	Grant     *Card          `json:"grant,omitempty"`
}

// PendingCard returns the card if this entry is one that still awaits the user.
func (e *Entry) PendingCard() *PendingCard {
	if e.Kind != "send-message" || e.Message == nil {
		return nil
	}
	m := e.Message
	pc := &PendingCard{EntryID: e.ID, Type: m.Type}
	switch m.Type {
	case "auto-review-approval", "local-tool-permission":
		ap := e.PendingApproval()
		if ap == nil {
			return nil
		}
		pc.Kind, pc.RequestID, pc.Approval = "approval", ap.RequestID, ap.Card
	case "widget":
		if m.Widget == nil || e.RespondedValue != nil || e.WidgetDismissed || e.WidgetSkipped {
			return nil
		}
		pc.Kind, pc.Widget = "widget", m.Widget
	case "user-form":
		if m.FormRequest == nil || e.FormResolution != "" {
			return nil
		}
		pc.Kind, pc.Form = "form", m.FormRequest
	case "secret-request":
		if m.SecretRequest == nil || e.SecretProvided {
			return nil
		}
		pc.Kind, pc.Secret = "secret", m.SecretRequest
	case "connector-grant":
		if m.Ask == nil || m.Ask.Status != "pending" {
			return nil
		}
		pc.Kind, pc.RequestID, pc.Grant = "grant", m.Ask.RequestID, m.Ask
	default:
		return nil
	}
	return pc
}

// Cards scans a bot's recent transcript for cards awaiting an answer.
func (a *API) Cards(ctx context.Context, ag *Agent) ([]PendingCard, error) {
	p, err := a.Tail(ctx, ag.ID, 200, 0)
	if err != nil {
		return nil, err
	}
	var out []PendingCard
	for i := range p.Entries {
		if c := p.Entries[i].PendingCard(); c != nil {
			c.AgentID, c.AgentName = ag.ID, ag.Name
			out = append(out, *c)
		}
	}
	return out, nil
}

// FindCard returns one pending card by entry id (fresh from the transcript, so stale answers are refused).
func (a *API) FindCard(ctx context.Context, ag *Agent, entryID string) (*PendingCard, error) {
	cards, err := a.Cards(ctx, ag)
	if err != nil {
		return nil, err
	}
	for i := range cards {
		if cards[i].EntryID == entryID || strings.HasPrefix(cards[i].EntryID, entryID) && len(entryID) >= 3 {
			return &cards[i], nil
		}
	}
	return nil, fmt.Errorf("no pending card %q on %s (already answered, expired, or older than 200 entries)", entryID, ag.Name)
}

// CardResponse is one answer to a card.
//
//	approval: approve | always | deny | never   (+ Command to approve an edited command)
//	widget:   answer (Values: option labels/values/1-based numbers, or Custom) | dismiss
//	form:     submit (Fields: id or label → value) | dismiss
//	secret:   submit (Value)
//	grant:    allow | allow-bot | allow-team | skip
type CardResponse struct {
	Action  string            `json:"action"`
	Values  []string          `json:"values,omitempty"`
	Custom  string            `json:"custom,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
	Value   string            `json:"value,omitempty"`
	Command string            `json:"command,omitempty"`
}

// Respond answers a card. It never acts on a card that is no longer pending.
func (a *API) Respond(ctx context.Context, ag *Agent, c *PendingCard, r CardResponse) error {
	base := map[string]any{"agentId": ag.ID, "entryId": c.EntryID}
	with := func(kv ...any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	switch c.Kind {
	case "approval":
		if c.Type == "local-tool-permission" {
			res := map[string]string{"approve": "allow-once", "always": "always", "deny": "deny", "never": "never"}[r.Action]
			if res == "" {
				return errors.New("approval actions: approve | always | deny | never")
			}
			return a.Call(ctx, "resolveLocalToolPermission", with("requestId", c.RequestID, "resolution", res), nil)
		}
		res := map[string]string{"approve": "approved", "always": "always", "deny": "denied", "never": "denied"}[r.Action]
		if res == "" {
			return errors.New("approval actions: approve | always | deny")
		}
		args := with("requestId", c.RequestID, "resolution", res, "approvalPlatform", "desktop")
		if r.Command != "" && res != "denied" {
			args["approvedCommand"] = r.Command
		}
		return a.Call(ctx, "resolveAutoReviewApproval", args, nil)

	case "widget":
		if r.Action == "dismiss" {
			return a.Call(ctx, "dismissWidget", base, nil)
		}
		var picks []string
		for _, v := range r.Values {
			opt, err := pickOption(c.Widget, v)
			if err != nil {
				return err
			}
			picks = append(picks, opt)
		}
		if r.Custom != "" {
			if !c.Widget.AllowCustom && len(c.Widget.Options) > 0 {
				return errors.New("this question does not accept a custom answer")
			}
			picks = append(picks, r.Custom)
		}
		if len(picks) == 0 {
			return errors.New("choose at least one option")
		}
		if len(picks) > 1 && !c.Widget.MultiSelect {
			return errors.New("this question takes a single answer")
		}
		// Multi-select answers are newline-joined, exactly as the app does.
		var out struct {
			Accepted *bool `json:"accepted"`
		}
		if err := a.Call(ctx, "respondToWidget", with("value", strings.Join(picks, "\n")), &out); err != nil {
			return err
		}
		if out.Accepted != nil && !*out.Accepted {
			return errors.New("the bot did not accept the answer (the question may have moved on)")
		}
		return nil

	case "form":
		if r.Action == "dismiss" {
			return a.Call(ctx, "dismissUserForm", with("mode", "dismissed", "platform", "desktop"), nil)
		}
		values := map[string]any{}
		for k, v := range r.Fields {
			id := ""
			for _, f := range c.Form.Fields {
				if f.ID == k || strings.EqualFold(f.Label, k) {
					id = f.ID
				}
			}
			if id == "" {
				return fmt.Errorf("form has no field %q", k)
			}
			values[id] = v
		}
		for _, f := range c.Form.Fields {
			if _, ok := values[f.ID]; f.Required && !ok {
				return fmt.Errorf("field %q (%s) is required", f.ID, f.Label)
			}
		}
		return a.Call(ctx, "submitUserForm", with("values", values, "platform", "desktop"), nil)

	case "secret":
		if r.Value == "" {
			return errors.New("secret value is empty")
		}
		return a.Call(ctx, "submitSecret", with("value", r.Value), nil)

	case "grant":
		res := map[string]string{"allow": "allowed", "allow-bot": "always_this_bot", "allow-team": "always_all_team_bots", "skip": "skipped"}[r.Action]
		if res == "" {
			return errors.New("grant actions: allow | allow-bot | allow-team | skip")
		}
		return a.Call(ctx, "resolveConnectorGrant", with("requestId", c.RequestID, "resolution", res), nil)
	}
	return fmt.Errorf("unsupported card kind %q", c.Kind)
}

// pickOption matches a 1-based number, an option value or a label.
func pickOption(w *Widget, v string) (string, error) {
	if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(w.Options) {
		return w.Options[n-1].Answer(), nil
	}
	for _, o := range w.Options {
		if o.Value == v || strings.EqualFold(o.Label, v) {
			return o.Answer(), nil
		}
	}
	if w.AllowCustom {
		return v, nil
	}
	return "", fmt.Errorf("%q is not one of the options", v)
}
