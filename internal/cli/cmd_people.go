package cli

// Group chats with people (Cursor team members) live on the Cursor backend, not on the box:
//   people:  DashboardService/GetTeams → GetTeamMembers {teamId}            (header x-cursor-team-id)
//   create:  GrokBotService/CreateGrokBotRoom {agentId, name, description, memberAgentIds, humanMemberUserIds}
//   invite:  GrokBotService/AddGrokBotRoomPeople    {agentId, userIds}
//   remove:  GrokBotService/RemoveGrokBotRoomPeople {agentId, userIds}   (your own id = leave)
//   bots:    GrokBotService/SetGrokBotRoomMembers   {agentId, memberAgentIds}

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gb/internal/api"
	"gb/internal/auth"
	"gb/internal/box"
)

const (
	maxRoomBotsWithPeople = 3
	maxRoomPeople         = 20 // including you
)

type teamMember struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	IsRemoved bool   `json:"isRemoved,omitempty"`
}

type team struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// teamContext returns the team to use (--team, the logged-in selected team, or the first team) and its members.
func teamContext(ctx context.Context, e *env, x *api.API) (*team, []teamMember, int64, error) {
	var teams struct {
		Teams []team `json:"teams"`
	}
	if err := box.Backend(ctx, x.P, "aiserver.v1.DashboardService/GetTeams", map[string]any{}, &teams); err != nil {
		return nil, nil, 0, err
	}
	if len(teams.Teams) == 0 {
		return nil, nil, 0, errors.New("this account has no Cursor team; group chats with people need a team account (cursor.com/dashboard)")
	}
	want := e.a.get("team", "")
	if want == "" {
		if c, _ := x.P.Credentials(); c != nil {
			want = c.TeamID
		}
	}
	t := &teams.Teams[0]
	for i := range teams.Teams {
		if want != "" && (strconv.FormatInt(teams.Teams[i].ID, 10) == want || strings.EqualFold(teams.Teams[i].Name, want)) {
			t = &teams.Teams[i]
		}
	}
	var res struct {
		TeamMembers []teamMember `json:"teamMembers"`
		UserID      int64        `json:"userId"`
	}
	if err := box.BackendTeam(ctx, x.P, strconv.FormatInt(t.ID, 10), "aiserver.v1.DashboardService/GetTeamMembers", map[string]any{"teamId": t.ID}, &res); err != nil {
		return nil, nil, 0, err
	}
	members := res.TeamMembers[:0]
	for _, m := range res.TeamMembers {
		if !m.IsRemoved {
			members = append(members, m)
		}
	}
	return t, members, res.UserID, nil
}

// personIDs resolves people by numeric id, email or name (case-insensitive) among team members.
func personIDs(members []teamMember, refs []string) ([]int64, error) {
	var out []int64
	for _, r := range refs {
		r = strings.TrimSpace(r)
		var hit []teamMember
		for _, m := range members {
			if strconv.FormatInt(m.ID, 10) == r || strings.EqualFold(m.Email, r) || strings.EqualFold(m.Name, r) {
				hit = append(hit, m)
			}
		}
		switch len(hit) {
		case 0:
			return nil, fmt.Errorf("no team member %q (gb people)", r)
		case 1:
			out = append(out, hit[0].ID)
		default:
			return nil, fmt.Errorf("%q matches %d team members; use the email or id", r, len(hit))
		}
	}
	return out, nil
}

func cmdPeople(ctx context.Context, e *env) error {
	x, err := e.API()
	if err != nil {
		return err
	}
	t, members, me, err := teamContext(ctx, e, x)
	if err != nil {
		return err
	}
	e.emit(map[string]any{"team": t, "you": me, "members": members}, func(w io.Writer) {
		fmt.Fprintf(w, "Team %s (%d)\n", t.Name, t.ID)
		rows := [][]string{}
		for _, m := range members {
			you := ""
			if m.ID == me {
				you = "(you)"
			}
			rows = append(rows, []string{strconv.FormatInt(m.ID, 10), m.Name, m.Email, you})
		}
		table(w, []string{"ID", "NAME", "EMAIL", ""}, rows)
	})
	return nil
}

// autoRoomName mirrors the app: bot names joined like "A and B" / "A, B, and C", at most 72 runes.
// The name is sent without namedBy so the desktop keeps recomputing it when members change.
func autoRoomName(names []string) string {
	var s string
	switch len(names) {
	case 0:
		s = "New group"
	case 1:
		s = names[0]
	case 2:
		s = names[0] + " and " + names[1]
	default:
		s = strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 72 {
		s = string(r[:72])
	}
	return s
}

// createRoomWithPeople creates a server-hosted group chat with team members.
func createRoomWithPeople(ctx context.Context, e *env, x *api.API, name, description string, bots []*api.Agent, people []string) (*api.Agent, error) {
	if len(bots) > maxRoomBotsWithPeople {
		return nil, fmt.Errorf("a group with people can have at most %d bots", maxRoomBotsWithPeople)
	}
	if len(people)+1 > maxRoomPeople {
		return nil, fmt.Errorf("a group can have at most %d people including you", maxRoomPeople)
	}
	t, members, _, err := teamContext(ctx, e, x)
	if err != nil {
		return nil, err
	}
	ids, err := personIDs(members, people)
	if err != nil {
		return nil, err
	}
	botIDs := []string{}
	for _, b := range bots {
		if b.Harness != "temporal" {
			return nil, fmt.Errorf("%q runs on your box; group chats with people need server-hosted bots", b.Name)
		}
		botIDs = append(botIDs, b.ID)
	}
	agentID := auth.UUID()
	var res struct {
		Agent struct {
			AgentID string `json:"agentId"`
			Kind    string `json:"kind"`
		} `json:"agent"`
	}
	if err := box.BackendTeam(ctx, x.P, strconv.FormatInt(t.ID, 10), "aiserver.v1.GrokBotService/CreateGrokBotRoom", map[string]any{
		"agentId": agentID, "name": name, "description": description,
		"memberAgentIds": botIDs, "humanMemberUserIds": ids,
	}, &res); err != nil {
		return nil, err
	}
	if res.Agent.AgentID != "" && res.Agent.AgentID != agentID {
		return nil, fmt.Errorf("server created room %s, expected %s", res.Agent.AgentID, agentID)
	}
	x.Invalidate()
	if ag, err := x.Resolve(ctx, agentID); err == nil {
		return ag, nil
	}
	return &api.Agent{ID: agentID, Name: name, IsGroup: true, MemberIDs: botIDs}, nil
}

// groupPeopleCmd handles: groups people|invite|kick|leave <group> [person...]
func groupPeopleCmd(ctx context.Context, e *env, sub string) error {
	if len(e.a.pos) < 1 || (sub == "invite" || sub == "kick") && len(e.a.pos) < 2 {
		return usage("gb groups %s <group> [person (id|email|name)...]", sub)
	}
	x, g, err := e.target(ctx, e.a.pos[0])
	if err != nil {
		return err
	}
	if !g.IsGroup {
		return fmt.Errorf("%q is a bot, not a group", g.Name)
	}
	if sub == "people" {
		var people []map[string]any
		_ = jsonRoundTrip(g.People, &people)
		e.emit(people, func(w io.Writer) {
			if len(people) == 0 {
				fmt.Fprintln(w, "Only you (no people invited).")
				return
			}
			rows := [][]string{}
			for _, p := range people {
				rows = append(rows, []string{fmt.Sprint(p["userId"]), fmt.Sprint(p["displayName"])})
			}
			table(w, []string{"USER ID", "NAME"}, rows)
		})
		return nil
	}
	t, members, me, err := teamContext(ctx, e, x)
	if err != nil {
		return err
	}
	var ids []int64
	method := "aiserver.v1.GrokBotService/AddGrokBotRoomPeople"
	switch sub {
	case "invite":
		ids, err = personIDs(members, e.a.pos[1:])
	case "kick":
		method = "aiserver.v1.GrokBotService/RemoveGrokBotRoomPeople"
		ids, err = personIDs(members, e.a.pos[1:])
	case "leave":
		method, ids = "aiserver.v1.GrokBotService/RemoveGrokBotRoomPeople", []int64{me}
	}
	if err != nil {
		return err
	}
	var out any
	if err := box.BackendTeam(ctx, x.P, strconv.FormatInt(t.ID, 10), method, map[string]any{"agentId": g.ID, "userIds": ids}, &out); err != nil {
		return err
	}
	x.Invalidate()
	e.emit(map[string]any{"group": g.ID, "action": sub, "userIds": ids}, func(w io.Writer) {
		fmt.Fprintf(w, "%s: %s %v\n", g.Name, sub, ids)
	})
	return nil
}

func jsonRoundTrip(raw []byte, v any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// hasPeople reports whether a group includes humans besides the owner (managed on the backend).
func hasPeople(g *api.Agent) bool {
	var people []any
	_ = jsonRoundTrip(g.People, &people)
	return len(people) > 1
}
