// Package api wraps the box gateway methods (see docs/GATEWAY_METHODS.md) with typed helpers,
// a cached name→id resolver, and transcript helpers.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gb/internal/auth"
	"gb/internal/box"
	"gb/internal/config"
)

type Agent struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	Title                  string   `json:"title,omitempty"`
	Description            string   `json:"description"`
	AvatarShape            string   `json:"avatarShape,omitempty"`
	AvatarColor            string   `json:"avatarColor,omitempty"`
	IsGroup                bool     `json:"isGroup"`
	MemberIDs              []string `json:"memberIds"`
	Origin                 string   `json:"origin,omitempty"`
	Harness                string   `json:"harness,omitempty"`
	IsRunning              bool     `json:"isRunning"`
	IsRunningTurn          *bool    `json:"isRunningTurn,omitempty"`
	IsComposingMessage     bool     `json:"isComposingMessage,omitempty"`
	HasUnread              bool     `json:"hasUnread"`
	UnreadCount            int      `json:"unreadCount,omitempty"`
	NotifyOnUpdatesEnabled bool     `json:"notifyOnUpdatesEnabled"`
	IsHiddenFromSidebar    bool     `json:"isHiddenFromSidebar,omitempty"`
	DraftingDisabled       bool     `json:"draftingDisabled,omitempty"`
	LastMessagePreview     string   `json:"lastMessagePreview,omitempty"`
	CreatedAt              float64  `json:"createdAt"`
	UpdatedAt              float64  `json:"updatedAt"`
	// People are the humans in a group chat ({userId, displayName, isViewer, ...}).
	People json.RawMessage `json:"people,omitempty"`
	// AwaitingUserResponse is an object when the bot waits on the user (approval, form, ...).
	AwaitingUserResponse json.RawMessage `json:"awaitingUserResponse,omitempty"`
}

func (a *Agent) Busy() bool {
	return a.IsRunning || (a.IsRunningTurn != nil && *a.IsRunningTurn)
}

func (a *Agent) Awaiting() bool {
	s := strings.TrimSpace(string(a.AwaitingUserResponse))
	return s != "" && s != "null" && s != "false"
}

type API struct {
	*box.Client
	mu    sync.Mutex
	cache []Agent
}

func Open(profile string) (*API, error) {
	p, err := config.Open(profile)
	if err != nil {
		return nil, err
	}
	return &API{Client: box.New(p)}, nil
}

// ---------- agents (bots & groups) ----------

type agentsCache struct {
	SavedAtMs int64   `json:"savedAtMs"`
	Agents    []Agent `json:"agents"`
}

const cacheTTL = 60 * time.Second

// Agents lists bots and groups. fresh=false allows a ≤60s on-disk cache (refs resolve without a round-trip).
func (a *API) Agents(ctx context.Context, fresh bool) ([]Agent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !fresh {
		if a.cache != nil {
			return a.cache, nil
		}
		var c agentsCache
		if a.P.ReadJSON(config.AgentsCache, &c) == nil && time.Since(time.UnixMilli(c.SavedAtMs)) < cacheTTL && os.Getenv("GB_NO_CACHE") == "" {
			a.cache = c.Agents
			return c.Agents, nil
		}
	}
	var list []Agent
	if err := a.Call(ctx, "listAgents", nil, &list); err != nil {
		return nil, err
	}
	a.cache = list
	_ = a.P.WriteJSON(config.AgentsCache, agentsCache{SavedAtMs: time.Now().UnixMilli(), Agents: list})
	return list, nil
}

// SetRoster replaces the in-memory roster (the daemon keeps it current from the event stream).
func (a *API) SetRoster(list []Agent) {
	a.mu.Lock()
	a.cache = list
	a.mu.Unlock()
}

func (a *API) Invalidate() {
	a.mu.Lock()
	a.cache = nil
	a.mu.Unlock()
	_ = a.P.Remove(config.AgentsCache)
}

// Resolve finds a bot/group by id, exact name/title (case-insensitive) or unique prefix.
func (a *API) Resolve(ctx context.Context, ref string) (*Agent, error) {
	for attempt := 0; attempt < 2; attempt++ {
		list, err := a.Agents(ctx, attempt == 1)
		if err != nil {
			return nil, err
		}
		if ag, err := match(list, ref); err == nil || attempt == 1 {
			return ag, err
		}
	}
	return nil, nil
}

func match(list []Agent, ref string) (*Agent, error) {
	needle := strings.ToLower(strings.TrimSpace(ref))
	if needle == "" {
		return nil, errors.New("empty bot reference")
	}
	var exact, prefix []*Agent
	for i := range list {
		ag := &list[i]
		if strings.ToLower(ag.ID) == needle {
			return ag, nil
		}
		n, t := strings.ToLower(ag.Name), strings.ToLower(ag.Title)
		if n == needle || (t != "" && t == needle) {
			exact = append(exact, ag)
		} else if strings.HasPrefix(n, needle) || strings.HasPrefix(strings.ToLower(ag.ID), needle) {
			prefix = append(prefix, ag)
		}
	}
	pick := exact
	if len(pick) == 0 {
		pick = prefix
	}
	switch len(pick) {
	case 1:
		return pick[0], nil
	case 0:
		return nil, fmt.Errorf("no bot or group matches %q (gb bots list)", ref)
	}
	names := make([]string, 0, len(pick))
	for _, p := range pick {
		names = append(names, p.Name+" ("+p.ID[:min(8, len(p.ID))]+")")
	}
	return nil, fmt.Errorf("%q is ambiguous: %s — use the id", ref, strings.Join(names, ", "))
}

type CreateResult struct {
	Agent      Agent             `json:"agent"`
	Transcript []json.RawMessage `json:"transcript"`
}

func (a *API) CreateAgent(ctx context.Context, args map[string]any) (*Agent, error) {
	if _, ok := args["description"]; !ok {
		args["description"] = ""
	}
	args["clientNonce"] = auth.UUID()
	if _, ok := args["origin"]; !ok {
		args["origin"] = "user"
	}
	var r CreateResult
	if err := a.Call(ctx, "createAgent", args, &r); err != nil {
		return nil, err
	}
	a.Invalidate()
	return &r.Agent, nil
}

// Patch holds optional profile/settings changes; nil means unchanged.
type Patch struct {
	Name, Title, Description, AvatarShape, AvatarColor *string
	Notify, Hidden, DraftingOff                        *bool
}

// UpdateAgent merges the patch into the full profile (updateAgent is not a partial update).
func (a *API) UpdateAgent(ctx context.Context, ref string, p Patch) (*Agent, error) {
	ag, err := a.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	if p.Name != nil || p.Title != nil || p.Description != nil || p.AvatarShape != nil || p.AvatarColor != nil {
		prof := map[string]any{"name": ag.Name, "description": ag.Description}
		set := func(k string, cur string, v *string) {
			if v != nil {
				prof[k] = *v
			} else if cur != "" {
				prof[k] = cur
			}
		}
		set("name", ag.Name, p.Name)
		set("description", ag.Description, p.Description)
		set("title", ag.Title, p.Title)
		set("avatarShape", ag.AvatarShape, p.AvatarShape)
		set("avatarColor", ag.AvatarColor, p.AvatarColor)
		if strings.TrimSpace(prof["name"].(string)) == "" {
			return nil, errors.New("name cannot be blank")
		}
		if err := a.Call(ctx, "updateAgent", map[string]any{"id": ag.ID, "profile": prof}, nil); err != nil {
			return nil, err
		}
	}
	if p.Notify != nil {
		if err := a.Call(ctx, "setAgentNotifyOnUpdates", map[string]any{"id": ag.ID, "isEnabled": *p.Notify}, nil); err != nil {
			return nil, err
		}
	}
	if p.Hidden != nil {
		if err := a.Call(ctx, "setAgentHiddenFromSidebar", map[string]any{"id": ag.ID, "isHidden": *p.Hidden}, nil); err != nil {
			return nil, err
		}
	}
	if p.DraftingOff != nil {
		if err := a.Call(ctx, "setAgentDraftingDisabled", map[string]any{"id": ag.ID, "isDisabled": *p.DraftingOff}, nil); err != nil {
			return nil, err
		}
	}
	a.Invalidate()
	return a.Resolve(ctx, ag.ID)
}

type DeleteResult struct {
	Deleted         []string `json:"deleted"`
	RefusedAgentIDs []string `json:"refusedAgentIds,omitempty"`
	FailedAgentIDs  []string `json:"failedAgentIds,omitempty"`
}

// DeleteAgents uses the client-facing plural method (singular deleteAgent is host-only on the box).
func (a *API) DeleteAgents(ctx context.Context, refs []string) (*DeleteResult, error) {
	ids := []string{}
	for _, r := range refs {
		ag, err := a.Resolve(ctx, r)
		if err != nil {
			return nil, err
		}
		ids = append(ids, ag.ID)
	}
	var r DeleteResult
	if err := a.Call(ctx, "deleteAgents", map[string]any{"ids": ids}, &r); err != nil {
		return nil, err
	}
	bad := map[string]bool{}
	for _, id := range append(r.RefusedAgentIDs, r.FailedAgentIDs...) {
		bad[id] = true
	}
	for _, id := range ids {
		if !bad[id] {
			r.Deleted = append(r.Deleted, id)
		}
	}
	a.Invalidate()
	if len(bad) > 0 {
		return &r, fmt.Errorf("refused=%v failed=%v", r.RefusedAgentIDs, r.FailedAgentIDs)
	}
	return &r, nil
}

const MaxGroupMembers = 6

func (a *API) memberIDs(ctx context.Context, refs []string) ([]string, error) {
	seen := map[string]bool{}
	ids := []string{}
	for _, r := range refs {
		ag, err := a.Resolve(ctx, r)
		if err != nil {
			return nil, err
		}
		if ag.IsGroup {
			return nil, fmt.Errorf("%q is a group; groups cannot be nested", ag.Name)
		}
		if !seen[ag.ID] {
			seen[ag.ID] = true
			ids = append(ids, ag.ID)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("a group needs at least one member bot")
	}
	if len(ids) > MaxGroupMembers {
		return nil, fmt.Errorf("a group can have at most %d bots", MaxGroupMembers)
	}
	return ids, nil
}

// CreateGroup creates a bots-only group. named=false omits namedBy so the app treats the name as generated.
func (a *API) CreateGroup(ctx context.Context, name, description string, members []string, named bool) (*Agent, error) {
	ids, err := a.memberIDs(ctx, members)
	if err != nil {
		return nil, err
	}
	args := map[string]any{"name": name, "description": description, "memberAgentIds": ids, "clientNonce": auth.UUID()}
	if named {
		args["namedBy"] = "user"
	}
	var r CreateResult
	if err := a.Call(ctx, "createGroup", args, &r); err != nil {
		return nil, err
	}
	a.Invalidate()
	return &r.Agent, nil
}

func (a *API) SetMembers(ctx context.Context, groupRef string, members []string) (*Agent, error) {
	g, err := a.Resolve(ctx, groupRef)
	if err != nil {
		return nil, err
	}
	if !g.IsGroup {
		return nil, fmt.Errorf("%q is a bot, not a group", g.Name)
	}
	ids, err := a.memberIDs(ctx, members)
	if err != nil {
		return nil, err
	}
	if err := a.Call(ctx, "setGroupMembers", map[string]any{"id": g.ID, "memberAgentIds": ids}, nil); err != nil {
		return nil, err
	}
	a.Invalidate()
	return a.Resolve(ctx, g.ID)
}

// ---------- messages ----------

type SendResult struct {
	Target      *Agent `json:"target"`
	ClientNonce string `json:"clientNonce"`
	// Delivery: accepted | pending | rejected | unknown
	Delivery    string `json:"delivery"`
	EchoEntryID string `json:"echoEntryId,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type SendOpts struct {
	ReplyToID       string
	AttachmentPaths []string
	AttachmentNames []string
	ClientNonce     string // reuse to resend idempotently
}

func (a *API) Send(ctx context.Context, ag *Agent, prompt string, o SendOpts) (*SendResult, error) {
	nonce := o.ClientNonce
	if nonce == "" {
		nonce = auth.UUID()
	}
	args := map[string]any{
		"agentId": ag.ID, "prompt": prompt, "clientNonce": nonce,
		"directAddressedAcceptance": true, "source": "desktop", "sendSource": "chat", "initiator": "user",
		"composedAtMs": time.Now().UnixMilli(),
	}
	if o.ReplyToID != "" {
		args["replyToId"] = o.ReplyToID
	}
	if len(o.AttachmentPaths) > 0 {
		args["attachmentPaths"], args["attachmentNames"] = o.AttachmentPaths, o.AttachmentNames
	}
	res := &SendResult{Target: ag, ClientNonce: nonce, Delivery: "unknown"}
	var r *struct {
		Accepted *bool `json:"accepted"`
	}
	err := a.Call(ctx, "sendPrompt", args, &r)
	if err != nil {
		var ge *box.GatewayError
		if errors.As(err, &ge) && ge.Delivery == "rejected" {
			res.Delivery, res.Reason = "rejected", ge.Error()
			return res, err
		}
	} else if r != nil && r.Accepted != nil {
		if *r.Accepted {
			res.Delivery = "accepted"
		} else {
			res.Delivery = "rejected"
		}
	}
	// Confirm through the durable acceptance record whenever the reply was not conclusive.
	if res.Delivery == "unknown" {
		if st, lerr := a.Acceptance(ctx, ag.ID, nonce); lerr == nil {
			res.Delivery, res.EchoEntryID = st.Delivery, st.EchoEntryID
			if st.Reason != "" {
				res.Reason = st.Reason
			}
		}
	}
	if err != nil && res.Delivery == "unknown" {
		res.Reason = err.Error() + " (delivery unknown: check `gb log` before resending, or resend with --nonce " + nonce + ")"
		return res, err
	}
	return res, nil
}

type acceptance struct {
	Delivery, EchoEntryID, Reason string
}

// Acceptance looks up the durable prompt-acceptance record. "not-found" maps to "rejected-not-received"
// which is the only state where resending (with the same nonce) is safe.
func (a *API) Acceptance(ctx context.Context, agentID, nonce string) (*acceptance, error) {
	var r struct {
		Outcome string `json:"outcome"`
		Record  *struct {
			Status        string  `json:"status"`
			EchoEntryID   *string `json:"echoEntryId"`
			RejectionCode *string `json:"rejectionCode"`
		} `json:"record"`
	}
	if err := a.Call(ctx, "promptAcceptanceStatus", map[string]any{"accountSlot": "host", "agentId": agentID, "clientNonce": nonce}, &r); err != nil {
		return nil, err
	}
	out := &acceptance{Delivery: "unknown"}
	switch r.Outcome {
	case "found":
		out.Delivery = r.Record.Status
		if r.Record.EchoEntryID != nil {
			out.EchoEntryID = *r.Record.EchoEntryID
		}
		if r.Record.RejectionCode != nil {
			out.Reason = *r.Record.RejectionCode
		}
	case "not-found":
		out.Delivery, out.Reason = "not-received", "the box has no record of this prompt; safe to resend with the same nonce"
	}
	return out, nil
}

// ---------- transcript ----------

type Page struct {
	Entries       []Entry  `json:"entries"`
	NextBeforeSeq *float64 `json:"nextBeforeSeq,omitempty"`
}

func (a *API) Tail(ctx context.Context, id string, limit int, beforeSeq float64) (*Page, error) {
	args := map[string]any{"id": id, "limit": limit}
	if beforeSeq > 0 {
		args["beforeSeq"] = beforeSeq
	}
	var p Page
	return &p, a.Call(ctx, "getAgentTranscriptTail", args, &p)
}

func (a *API) Thread(ctx context.Context, id, rootID string) (*Page, error) {
	var p Page
	return &p, a.Call(ctx, "getAgentThread", map[string]any{"id": id, "rootId": rootID}, &p)
}
