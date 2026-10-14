// Package daemon implements gbd: one long-lived gateway connection shared by a local HTTP API,
// a normalized event stream and outgoing webhooks.
//
// Concurrency model: there is no queue and no per-bot lock. Every send is forwarded to the gateway
// immediately (Grok merges input that arrives mid-turn into the running turn), and every bot is
// independent, so bot2 never waits for bot1. The only serialized work is the per-bot gap repair.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gb/internal/api"
	"gb/internal/box"
)

// Event is the normalized event sent to /v1/events subscribers and webhooks.
type Event struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"` // message | prompt | activity | turn.done | approval | draft | widget | agent.updated | agent.deleted
	TimeMs    int64  `json:"ts"`
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName,omitempty"`

	// message / prompt / approval / draft / widget
	EntryID string          `json:"entryId,omitempty"`
	Kind    string          `json:"kind,omitempty"` // transcript entry kind
	Text    string          `json:"text,omitempty"`
	FromBot bool            `json:"fromBot,omitempty"`
	Author  string          `json:"author,omitempty"`
	ReplyTo string          `json:"replyTo,omitempty"`
	Entry   json.RawMessage `json:"entry,omitempty"`
	// Refs are the caller references (e.g. Discord message ids) of prompts sent to this bot since its
	// last finished turn. Grok merges mid-turn input into one turn, so its replies answer all of them.
	Refs []string `json:"refs,omitempty"`
	// Ref/Nonce identify the prompt on "prompt" events (echo of a send).
	Ref   string `json:"ref,omitempty"`
	Nonce string `json:"nonce,omitempty"`

	// activity / turn.done
	Running   *bool `json:"running,omitempty"`
	Composing *bool `json:"composing,omitempty"`

	Approval *api.Approval    `json:"approval,omitempty"`
	Card     *api.PendingCard `json:"card,omitempty"`  // approval | card events: anything waiting for an answer
	Files    []api.File       `json:"files,omitempty"` // message events: files the bot attached (GET /v1/files)
	Alert    json.RawMessage  `json:"alert,omitempty"` // alert events: the app's error tray
	Agent    *api.Agent       `json:"agent,omitempty"`
}

type agentState struct {
	running, composing bool
	replica, epoch     string
	seq                float64
	seen               map[string]string // entryId -> body (dedupe appended/updated)
	refs               []string          // refs of prompts sent since the last finished turn
	lastRefs           []string          // refs of the turn that just finished (late replies still route there)
	doneTimer          *time.Timer
	repairing          bool
	maxTs              float64 // newest entry timestamp handled; repairs never re-emit older entries
}

type sendInfo struct {
	ref     string
	agentID string
	at      time.Time
}

type Hub struct {
	API *api.API
	Log *log.Logger

	mu     sync.Mutex
	agents map[string]*api.Agent
	state  map[string]*agentState
	nonces map[string]sendInfo // clientNonce -> caller ref

	subMu  sync.Mutex
	subs   map[int]chan Event
	nextID int
	ring   []Event // last N events for ?since= replay
	lastID int64

	connected bool
	startMs   float64
}

const ringSize = 1000

func NewHub(x *api.API, lg *log.Logger) *Hub {
	return &Hub{API: x, Log: lg, agents: map[string]*api.Agent{}, state: map[string]*agentState{},
		nonces: map[string]sendInfo{}, subs: map[int]chan Event{}, startMs: float64(time.Now().UnixMilli())}
}

func (h *Hub) st(id string) *agentState {
	s := h.state[id]
	if s == nil {
		s = &agentState{seen: map[string]string{}, maxTs: h.startMs}
		h.state[id] = s
	}
	return s
}

// Subscribe registers a listener; since>0 replays buffered events with a larger id first.
func (h *Hub) Subscribe(since int64) (int, <-chan Event, []Event) {
	h.subMu.Lock()
	defer h.subMu.Unlock()
	h.nextID++
	ch := make(chan Event, 256)
	h.subs[h.nextID] = ch
	var replay []Event
	if since > 0 {
		for _, e := range h.ring {
			if e.ID > since {
				replay = append(replay, e)
			}
		}
	}
	return h.nextID, ch, replay
}

func (h *Hub) Unsubscribe(id int) {
	h.subMu.Lock()
	defer h.subMu.Unlock()
	if ch, ok := h.subs[id]; ok {
		close(ch)
		delete(h.subs, id)
	}
}

func (h *Hub) publish(e Event) {
	h.subMu.Lock()
	defer h.subMu.Unlock()
	h.lastID++
	e.ID = h.lastID
	if e.TimeMs == 0 {
		e.TimeMs = time.Now().UnixMilli()
	}
	h.ring = append(h.ring, e)
	if len(h.ring) > ringSize {
		h.ring = h.ring[len(h.ring)-ringSize:]
	}
	for id, ch := range h.subs {
		select {
		case ch <- e:
		default:
			h.Log.Printf("subscriber %d too slow; dropping it", id)
			close(ch)
			delete(h.subs, id)
		}
	}
}

func (h *Hub) name(id string) string {
	if a := h.agents[id]; a != nil {
		return a.Name
	}
	return ""
}

// Roster returns a snapshot of known bots and groups.
func (h *Hub) Roster() []api.Agent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]api.Agent, 0, len(h.agents))
	for _, a := range h.agents {
		out = append(out, *a)
	}
	return out
}

// NoteSend records the caller reference for a prompt so replies can be routed back.
func (h *Hub) NoteSend(agentID, nonce, ref string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nonces[nonce] = sendInfo{ref: ref, agentID: agentID, at: time.Now()}
	if ref != "" {
		s := h.st(agentID)
		s.refs = appendUnique(s.refs, ref)
	}
	for n, si := range h.nonces { // bounded memory
		if time.Since(si.at) > 24*time.Hour {
			delete(h.nonces, n)
		}
	}
}

// ForgetSend drops the ref of a prompt the gateway rejected, so no turn reports it.
func (h *Hub) ForgetSend(agentID, nonce, ref string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.nonces, nonce)
	if ref != "" {
		s := h.st(agentID)
		s.refs = remove(s.refs, ref)
	}
}

func remove(list []string, v string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// Run keeps the event stream open until ctx ends.
func (h *Hub) Run(ctx context.Context) error {
	if list, err := h.API.Agents(ctx, true); err == nil {
		h.setRoster(list, false)
	} else {
		h.Log.Printf("initial roster: %v", err)
	}
	var lastEvent time.Time // only touched from the stream goroutine
	return h.API.Events(ctx, func() {
		h.mu.Lock()
		h.connected = true
		h.mu.Unlock()
		h.Log.Printf("event stream connected")
		if !lastEvent.IsZero() {
			// Reconnected: refresh the roster and repair bots that changed while we were away.
			since := lastEvent
			go func() {
				if list, err := h.API.Agents(ctx, true); err == nil {
					h.setRoster(list, true)
				}
				h.repairChanged(ctx, since)
			}()
		}
	}, func(ev box.Event) {
		h.handle(ctx, ev)
		lastEvent = time.Now()
	})
}

func (h *Hub) Connected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.connected
}

func (h *Hub) setRoster(list []api.Agent, emit bool) {
	h.mu.Lock()
	seen := map[string]bool{}
	for i := range list {
		a := list[i]
		seen[a.ID] = true
		h.agents[a.ID] = &a
		s := h.st(a.ID)
		s.running, s.composing = a.Busy(), false
	}
	var gone []string
	for id := range h.agents {
		if !seen[id] {
			gone = append(gone, id)
			delete(h.agents, id)
		}
	}
	h.mu.Unlock()
	h.API.SetRoster(list)
	if emit {
		for _, id := range gone {
			h.publish(Event{Type: "agent.deleted", AgentID: id})
		}
	}
}

func (h *Hub) repairChanged(ctx context.Context, since time.Time) {
	for _, a := range h.Roster() {
		if a.UpdatedAt >= float64(since.Add(-time.Minute).UnixMilli()) {
			h.repair(ctx, a.ID)
		}
	}
}

func (h *Hub) handle(ctx context.Context, ev box.Event) {
	switch ev.Channel {
	case "agents":
		var p struct {
			Agents []api.Agent `json:"agents"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Agents != nil {
			h.setRoster(p.Agents, true)
		}
	case "agent-upserted":
		var p struct {
			Agent *api.Agent `json:"agent"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Agent != nil {
			h.upsert(p.Agent)
		}
	case "transcript":
		h.transcript(ctx, ev.Payload)
	case "tray":
		var p struct {
			Type string          `json:"type"`
			Tray json.RawMessage `json:"tray"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Type == "pushed" {
			var t struct {
				AgentID string `json:"agentId"`
			}
			_ = json.Unmarshal(p.Tray, &t)
			h.mu.Lock()
			name := h.name(t.AgentID)
			h.mu.Unlock()
			h.publish(Event{Type: "alert", AgentID: t.AgentID, AgentName: name, Alert: p.Tray})
		}
	}
}

// turnDoneDelay lets trailing transcript events land first: for server-hosted bots the roster flips to
// idle slightly before the final message is appended.
const turnDoneDelay = 1500 * time.Millisecond

func (h *Hub) upsert(a *api.Agent) {
	h.mu.Lock()
	prev := h.agents[a.ID]
	cp := *a
	h.agents[a.ID] = &cp
	s := h.st(a.ID)
	running := a.Busy()
	composing := running && a.IsComposingMessage
	changed := running != s.running || composing != s.composing
	finished := s.running && !running
	if running && s.doneTimer != nil { // resumed before turn.done fired: same turn
		s.doneTimer.Stop()
		s.doneTimer = nil
	}
	s.running, s.composing = running, composing
	refs := append([]string(nil), s.refs...)
	if finished {
		id, name, turnRefs := a.ID, a.Name, refs
		s.doneTimer = time.AfterFunc(turnDoneDelay, func() {
			h.mu.Lock()
			st := h.st(id)
			if st.running {
				h.mu.Unlock()
				return
			}
			// Refs sent during the delay belong to the next turn.
			rest := st.refs
			for _, r := range turnRefs {
				rest = remove(rest, r)
			}
			refs := turnRefs
			st.lastRefs, st.refs, st.doneTimer = refs, rest, nil
			h.mu.Unlock()
			f := false
			h.publish(Event{Type: "turn.done", AgentID: id, AgentName: name, Running: &f, Refs: refs})
		})
	}
	h.mu.Unlock()

	h.API.SetRoster(h.Roster())
	if prev == nil || prev.Name != a.Name || prev.Description != a.Description || prev.Title != a.Title {
		h.publish(Event{Type: "agent.updated", AgentID: a.ID, AgentName: a.Name, Agent: &cp})
	}
	if changed {
		h.publish(Event{Type: "activity", AgentID: a.ID, AgentName: a.Name, Running: &running, Composing: &composing, Refs: refs})
	}
}

func (h *Hub) transcript(ctx context.Context, payload json.RawMessage) {
	var p struct {
		Type    string     `json:"type"`
		AgentID string     `json:"agentId"`
		Entry   *api.Entry `json:"entry"`
		Ordered *struct {
			ReplicaKey string  `json:"replicaKey"`
			Epoch      string  `json:"epoch"`
			Sequence   float64 `json:"sequence"`
		} `json:"ordered"`
	}
	if json.Unmarshal(payload, &p) != nil || p.AgentID == "" {
		return
	}
	gap := false
	h.mu.Lock()
	s := h.st(p.AgentID)
	if o := p.Ordered; o != nil {
		if s.replica == o.ReplicaKey && s.epoch == o.Epoch && s.seq > 0 && o.Sequence != s.seq+1 && o.Sequence > s.seq {
			gap = true
		}
		s.replica, s.epoch, s.seq = o.ReplicaKey, o.Epoch, o.Sequence
	}
	h.mu.Unlock()
	switch p.Type {
	case "appended", "updated":
		if p.Entry != nil {
			h.entry(p.AgentID, p.Entry, false)
		}
	case "snapshot", "cleared":
		gap = true
	}
	if gap {
		go h.repair(ctx, p.AgentID)
	}
}

// repair refetches a bot's tail and emits entries the stream skipped. One repair per bot at a time.
func (h *Hub) repair(ctx context.Context, agentID string) {
	h.mu.Lock()
	s := h.st(agentID)
	if s.repairing {
		h.mu.Unlock()
		return
	}
	s.repairing = true
	h.mu.Unlock()
	defer func() { h.mu.Lock(); s.repairing = false; h.mu.Unlock() }()
	page, err := h.API.Tail(ctx, agentID, 50, 0)
	if err != nil {
		h.Log.Printf("repair %s: %v", agentID, err)
		return
	}
	for i := range page.Entries {
		h.entry(agentID, &page.Entries[i], true)
	}
}

// entry converts a transcript entry into normalized events (once per distinct final body).
// repaired=true marks entries fetched from the tail: anything not newer than the watermark is history
// (e.g. after a reconnect or for a bot first seen now) and is recorded silently instead of re-emitted.
func (h *Hub) entry(agentID string, en *api.Entry, repaired bool) {
	if en.Pending() {
		return
	}
	body := en.Body()
	h.mu.Lock()
	s := h.st(agentID)
	if prev, ok := s.seen[en.ID]; ok && prev == body {
		h.mu.Unlock()
		return
	}
	if repaired && en.TimestampMs > 0 && en.TimestampMs <= s.maxTs {
		if _, ok := s.seen[en.ID]; !ok {
			s.seen[en.ID] = body
			h.mu.Unlock()
			return
		}
	}
	if en.TimestampMs > s.maxTs {
		s.maxTs = en.TimestampMs
	}
	_, existed := s.seen[en.ID]
	s.seen[en.ID] = body
	if len(s.seen) > 2000 { // bounded memory: forget the oldest half (ids are not ordered; good enough)
		n := 0
		for k := range s.seen {
			if n++; n > 1000 {
				break
			}
			delete(s.seen, k)
		}
	}
	name := h.name(agentID)
	refs := append([]string(nil), s.refs...)
	if len(refs) == 0 {
		refs = append(refs, s.lastRefs...)
	}
	var si sendInfo
	echo := false
	if en.ClientNonce != "" {
		si, echo = h.nonces[en.ClientNonce]
	}
	h.mu.Unlock()

	base := Event{AgentID: agentID, AgentName: name, EntryID: en.ID, Kind: en.Kind, Entry: en.Raw, ReplyTo: en.ReplyTo, TimeMs: int64(en.TimestampMs)}
	if !en.FromBot() {
		e := base
		e.Type, e.Text, e.Author = "prompt", body, en.Speaker(name)
		if echo {
			e.Ref, e.Nonce = si.ref, en.ClientNonce
		}
		if !existed {
			h.publish(e)
		}
		return
	}
	e := base
	e.FromBot, e.Text, e.Author, e.Refs = true, body, en.Speaker(name), refs
	e.Files = en.Files()
	if c := en.PendingCard(); c != nil {
		c.AgentID, c.AgentName = agentID, name
		e.Card = c
		if c.Kind == "approval" {
			ap := en.PendingApproval()
			ap.AgentID, ap.AgentName = agentID, name
			e.Type, e.Approval = "approval", ap
		} else {
			e.Type = "card"
		}
	} else if en.Message != nil && strings.HasSuffix(en.Message.Type, "-draft") {
		e.Type = "draft"
	} else if en.Message != nil && en.Message.Type == "widget" {
		e.Type = "widget"
	} else {
		e.Type = "message"
		if existed {
			e.Type = "message.updated"
		}
	}
	h.publish(e)
}

func (h *Hub) String() string { return fmt.Sprintf("hub(%d agents)", len(h.Roster())) }
