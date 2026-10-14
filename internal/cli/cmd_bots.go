package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"gb/internal/api"
	"gb/internal/box"
)

func status(a *api.Agent) string {
	switch {
	case a.Awaiting():
		return "needs you"
	case a.Busy():
		return "working"
	case a.HasUnread:
		return fmt.Sprintf("unread(%d)", a.UnreadCount)
	}
	return "idle"
}

func agentRows(list []api.Agent, all []api.Agent) [][]string {
	names := map[string]string{}
	for _, a := range all {
		names[a.ID] = a.Name
	}
	rows := [][]string{}
	for _, a := range list {
		extra := a.Title
		if a.IsGroup {
			m := []string{}
			for _, id := range a.MemberIDs {
				m = append(m, firstOr(names[id], short(id)))
			}
			extra = strings.Join(m, ", ")
		}
		host := "box"
		if a.Harness != "" {
			host = a.Harness
		}
		rows = append(rows, []string{short(a.ID), a.Name, status(&a), host, ago(a.UpdatedAt), trunc(extra, 40)})
	}
	return rows
}

func firstOr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func printAgent(w io.Writer, a *api.Agent, all []api.Agent) {
	fmt.Fprintf(w, "%s  %s\n", a.Name, a.ID)
	if a.Title != "" {
		fmt.Fprintf(w, "  title:        %s\n", a.Title)
	}
	if a.Description != "" {
		fmt.Fprintf(w, "  instructions: %s\n", trunc(a.Description, 200))
	}
	if a.IsGroup {
		rows := agentRows(filterIDs(all, a.MemberIDs), all)
		fmt.Fprintln(w, "  members:")
		for _, r := range rows {
			fmt.Fprintf(w, "    %s  %s  (%s)\n", r[0], r[1], r[2])
		}
	}
	fmt.Fprintf(w, "  status:       %s   notify=%v hidden=%v\n", status(a), a.NotifyOnUpdatesEnabled, a.IsHiddenFromSidebar)
	if a.AvatarShape != "" || a.AvatarColor != "" {
		fmt.Fprintf(w, "  avatar:       %s %s\n", a.AvatarColor, a.AvatarShape)
	}
	if a.LastMessagePreview != "" {
		fmt.Fprintf(w, "  last:         %s (%s ago)\n", trunc(a.LastMessagePreview, 100), ago(a.UpdatedAt))
	}
}

func filterIDs(all []api.Agent, ids []string) []api.Agent {
	out := []api.Agent{}
	for _, id := range ids {
		for _, a := range all {
			if a.ID == id {
				out = append(out, a)
			}
		}
	}
	return out
}

func cmdBots(ctx context.Context, e *env) error   { return agentsCmd(ctx, e, false) }
func cmdGroups(ctx context.Context, e *env) error { return agentsCmd(ctx, e, true) }

func agentsCmd(ctx context.Context, e *env, groups bool) error {
	kind := "bots"
	if groups {
		kind = "groups"
	}
	sub := "list"
	if len(e.a.pos) > 0 {
		sub, e.a.pos = e.a.pos[0], e.a.pos[1:]
	}
	x, err := e.API()
	if err != nil {
		return err
	}
	needRef := func() (string, error) {
		if len(e.a.pos) == 0 {
			return "", usage("gb %s %s <name|id>", kind, sub)
		}
		return e.a.pos[0], nil
	}
	switch sub {
	case "list", "ls":
		if e.a.bool("all-accounts") || e.a.bool("A") {
			return listAllAccounts(ctx, e, groups)
		}
		all, err := x.Agents(ctx, true)
		if err != nil {
			return err
		}
		list := []api.Agent{}
		for _, a := range all {
			if a.IsGroup == groups || e.a.bool("all") {
				list = append(list, a)
			}
		}
		sortAgents(list)
		e.emit(list, func(w io.Writer) {
			if len(list) == 0 {
				fmt.Fprintf(w, "No %s.\n", kind)
				return
			}
			last := "TITLE"
			if groups {
				last = "MEMBERS"
			}
			table(w, []string{"ID", "NAME", "STATUS", "HOST", "UPDATED", last}, agentRows(list, all))
		})
		return nil

	case "get", "show":
		ref, err := needRef()
		if err != nil {
			return err
		}
		x, a, err := e.target(ctx, ref)
		if err != nil {
			return err
		}
		x.Invalidate() // status must be live; the cache is only for name→id
		if a, err = x.Resolve(ctx, a.ID); err != nil {
			return err
		}
		all, _ := x.Agents(ctx, false)
		e.emit(a, func(w io.Writer) { printAgent(w, a, all) })
		return nil

	case "create", "new", "add":
		if groups && sub == "add" {
			break
		}
		name := e.a.get("name", strings.Join(e.a.pos, " "))
		if strings.TrimSpace(name) == "" && !groups {
			return usage("gb %s create <name> [--description TEXT] ...", kind)
		}
		var a *api.Agent
		if groups {
			var members []string
			if members, err = sameAccount(x, e.a.list("member")); err != nil {
				return err
			}
			if len(members) == 0 {
				return usage("gb groups create [name] --member BOT --member BOT [--person EMAIL] [--description TEXT]")
			}
			bots := []*api.Agent{}
			botNames := []string{}
			for _, m := range members {
				b, err := x.Resolve(ctx, m)
				if err != nil {
					return err
				}
				bots, botNames = append(bots, b), append(botNames, b.Name)
			}
			named := strings.TrimSpace(name) != ""
			if !named {
				name = autoRoomName(botNames) // app-style name, recomputed by the app when members change
			}
			if people := e.a.list("person"); len(people) > 0 {
				a, err = createRoomWithPeople(ctx, e, x, name, e.a.get("description", ""), bots, people)
			} else {
				a, err = x.CreateGroup(ctx, name, e.a.get("description", ""), members, named)
			}
			if err == nil && (e.a.optStr("title") != nil || e.a.optStr("avatar-shape") != nil || e.a.optStr("avatar-color") != nil) {
				a, err = x.UpdateAgent(ctx, a.ID, api.Patch{Title: e.a.optStr("title"), AvatarShape: e.a.optStr("avatar-shape"), AvatarColor: e.a.optStr("avatar-color")})
			}
		} else {
			args := map[string]any{"name": name, "description": e.a.get("description", "")}
			for flag, key := range map[string]string{"title": "title", "avatar-shape": "avatarShape", "avatar-color": "avatarColor", "template": "templateId", "language": "language"} {
				if v, ok := e.a.str(flag); ok {
					args[key] = v
				}
			}
			a, err = x.CreateAgent(ctx, args)
		}
		if err != nil {
			return err
		}
		e.emit(a, func(w io.Writer) { fmt.Fprintf(w, "Created %s %s (%s)\n", strings.TrimSuffix(kind, "s"), a.Name, a.ID) })
		return nil

	case "update", "edit", "set":
		if groups && sub == "set" {
			break
		}
		ref, err := needRef()
		if err != nil {
			return err
		}
		notify, err := e.a.onOff("notify")
		if err != nil {
			return err
		}
		hidden, err := e.a.onOff("hidden")
		if err != nil {
			return err
		}
		drafting, err := e.a.onOff("drafting")
		if err != nil {
			return err
		}
		var draftingOff *bool
		if drafting != nil {
			v := !*drafting
			draftingOff = &v
		}
		x, cur, err := e.target(ctx, ref)
		if err != nil {
			return err
		}
		a, err := x.UpdateAgent(ctx, cur.ID, api.Patch{
			Name: e.a.optStr("name"), Title: e.a.optStr("title"), Description: e.a.optStr("description"),
			AvatarShape: e.a.optStr("avatar-shape"), AvatarColor: e.a.optStr("avatar-color"),
			Notify: notify, Hidden: hidden, DraftingOff: draftingOff,
		})
		if err != nil {
			return err
		}
		e.emit(a, func(w io.Writer) { fmt.Fprintf(w, "Updated %s (%s)\n", a.Name, a.ID) })
		return nil

	case "delete", "rm":
		if len(e.a.pos) == 0 {
			return usage("gb %s delete <name|id> [more...]", kind)
		}
		// Group ids by account, then one deleteAgents call per account.
		byAcct := map[*api.API][]string{}
		for _, ref := range e.a.pos {
			y, ag, err := e.target(ctx, ref)
			if err != nil {
				return err
			}
			byAcct[y] = append(byAcct[y], ag.ID)
		}
		all := &api.DeleteResult{}
		var firstErr error
		for y, ids := range byAcct {
			r, err := y.DeleteAgents(ctx, ids)
			if r != nil {
				all.Deleted = append(all.Deleted, r.Deleted...)
				all.RefusedAgentIDs = append(all.RefusedAgentIDs, r.RefusedAgentIDs...)
				all.FailedAgentIDs = append(all.FailedAgentIDs, r.FailedAgentIDs...)
			}
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
		e.emit(all, func(w io.Writer) {
			if len(all.Deleted) > 0 {
				fmt.Fprintf(w, "Deleted %d: %s\n", len(all.Deleted), strings.Join(all.Deleted, ", "))
			}
		})
		return firstErr

	case "dup", "duplicate":
		ref, err := needRef()
		if err != nil {
			return err
		}
		x, src, err := e.target(ctx, ref)
		if err != nil {
			return err
		}
		var r api.CreateResult
		if err := x.Call(ctx, "duplicateAgent", map[string]any{"id": src.ID}, &r); err != nil {
			return err
		}
		x.Invalidate()
		e.emit(r.Agent, func(w io.Writer) { fmt.Fprintf(w, "Duplicated %s → %s (%s)\n", src.Name, r.Agent.Name, r.Agent.ID) })
		return nil
	}

	if groups {
		switch sub {
		case "people", "invite", "kick", "leave":
			return groupPeopleCmd(ctx, e, sub)
		case "members":
			ref, err := needRef()
			if err != nil {
				return err
			}
			x, g, err := e.target(ctx, ref)
			if err != nil {
				return err
			}
			all, _ := x.Agents(ctx, false)
			m := filterIDs(all, g.MemberIDs)
			e.emit(m, func(w io.Writer) {
				table(w, []string{"ID", "NAME", "STATUS", "HOST", "UPDATED", "TITLE"}, agentRows(m, all))
			})
			return nil
		case "add", "remove", "set":
			if len(e.a.pos) < 2 {
				return usage("gb groups %s <group> <bot> [bot...]", sub)
			}
			x, g, err := e.target(ctx, e.a.pos[0])
			if err != nil {
				return err
			}
			members, err := sameAccount(x, e.a.pos[1:])
			if err != nil {
				return err
			}
			switch sub {
			case "add":
				members = append(append([]string{}, g.MemberIDs...), members...)
			case "remove":
				drop := map[string]bool{}
				for _, r := range members {
					b, err := x.Resolve(ctx, r)
					if err != nil {
						return err
					}
					drop[b.ID] = true
				}
				members = nil
				for _, id := range g.MemberIDs {
					if !drop[id] {
						members = append(members, id)
					}
				}
			}
			var a *api.Agent
			if hasPeople(g) { // rooms with people are managed on the backend
				ids := []string{}
				for _, m := range members {
					b, err := x.Resolve(ctx, m)
					if err != nil {
						return err
					}
					ids = append(ids, b.ID)
				}
				if len(ids) > maxRoomBotsWithPeople {
					return fmt.Errorf("a group with people can have at most %d bots", maxRoomBotsWithPeople)
				}
				if err := box.Backend(ctx, x.P, "aiserver.v1.GrokBotService/SetGrokBotRoomMembers", map[string]any{"agentId": g.ID, "memberAgentIds": ids}, nil); err != nil {
					return err
				}
				x.Invalidate()
				a, err = x.Resolve(ctx, g.ID)
			} else {
				a, err = x.SetMembers(ctx, g.ID, members)
			}
			if err != nil {
				return err
			}
			e.emit(a, func(w io.Writer) { fmt.Fprintf(w, "%s now has %d members\n", a.Name, len(a.MemberIDs)) })
			return nil
		}
	}
	return usage("unknown subcommand %q (gb %s --help)", sub, kind)
}

// sameAccount strips "account:" prefixes and rejects members from another account (groups cannot span accounts).
func sameAccount(x *api.API, refs []string) ([]string, error) {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		acct, rest := splitRef(r)
		if acct != "" && acct != x.P.Name {
			return nil, fmt.Errorf("%q is in account %s; a group's members must all be in %s", r, acct, x.P.Name)
		}
		out = append(out, rest)
	}
	return out, nil
}

func listAllAccounts(ctx context.Context, e *env, groups bool) error {
	byAcct, names, err := e.allAccountsAgents(ctx)
	if err != nil {
		return err
	}
	type row struct {
		Account string `json:"account"`
		api.Agent
	}
	var out []row
	rows := [][]string{}
	for _, n := range names {
		all := byAcct[n]
		list := []api.Agent{}
		for _, a := range all {
			if a.IsGroup == groups || e.a.bool("all") {
				list = append(list, a)
			}
		}
		sortAgents(list)
		for i, r := range agentRows(list, all) {
			rows = append(rows, append([]string{n}, r...))
			out = append(out, row{n, list[i]})
		}
	}
	e.emit(out, func(w io.Writer) {
		last := "TITLE"
		if groups {
			last = "MEMBERS"
		}
		table(w, []string{"ACCOUNT", "ID", "NAME", "STATUS", "HOST", "UPDATED", last}, rows)
	})
	return nil
}
