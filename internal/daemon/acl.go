package daemon

import (
	"fmt"
	"strings"

	"gb/internal/api"
)

// ACL decides which principals may do what with which bots.
//
// Principals are opaque strings supplied by the front-end that authenticated the human, e.g.
// "discord:user:<id>", "discord:role:<id>", "discord:guild:<id>", "discord:channel:<id>".
// A request that carries no principals is the trusted caller itself and gets the principal "local".
// Front-ends acting for other people MUST pass their principals, otherwise they act as "local".
//
// Actions: send (send/ask), read (log), approve (approval & draft buttons), admin (new/stop/undo, raw call).
// Rules are additive (any matching rule allows); with no rules configured everything is allowed.
type ACL struct {
	Rules []Rule `json:"rules"`
}

type Rule struct {
	Principals []string `json:"principals"` // exact strings, "*" (anyone), or prefix patterns like "discord:role:*"
	Bots       []string `json:"bots"`       // "*", bot/group name or id, "account:name", "account:*"
	Actions    []string `json:"actions"`    // send | read | approve | admin | "*"
	Comment    string   `json:"comment,omitempty"`
}

var validActions = map[string]bool{"send": true, "read": true, "approve": true, "admin": true, "*": true}

func (a *ACL) Validate() error {
	if a == nil {
		return nil
	}
	for i, r := range a.Rules {
		if len(r.Principals) == 0 || len(r.Bots) == 0 || len(r.Actions) == 0 {
			return fmt.Errorf("acl rule %d: principals, bots and actions are all required", i)
		}
		for _, act := range r.Actions {
			if !validActions[act] {
				return fmt.Errorf("acl rule %d: unknown action %q", i, act)
			}
		}
	}
	return nil
}

func matchPattern(pat, v string) bool {
	if pat == "*" || pat == v {
		return true
	}
	if p, ok := strings.CutSuffix(pat, "*"); ok {
		return strings.HasPrefix(v, p)
	}
	return false
}

// matchBot: "*", id, name/title (case-insensitive), "account:name" or "account:*" for this daemon's account.
func matchBot(pat string, ag *api.Agent, account string) bool {
	if pat == "*" {
		return true
	}
	if ag == nil {
		return false
	}
	if acct, rest, ok := strings.Cut(pat, ":"); ok && acct == account {
		if rest == "*" {
			return true
		}
		pat = rest
	}
	return pat == ag.ID || strings.EqualFold(pat, ag.Name) || (ag.Title != "" && strings.EqualFold(pat, ag.Title))
}

// Allowed reports whether any principal may perform action on bot. ag == nil means "no specific bot"
// (only rules whose bots include "*" apply).
func (a *ACL) Allowed(principals []string, action string, ag *api.Agent, account string) bool {
	if a == nil || len(a.Rules) == 0 {
		return true
	}
	for _, r := range a.Rules {
		actOK := false
		for _, x := range r.Actions {
			actOK = actOK || x == "*" || x == action
		}
		if !actOK {
			continue
		}
		botOK := false
		for _, b := range r.Bots {
			if b == "*" || (ag != nil && matchBot(b, ag, account)) {
				botOK = true
			}
		}
		if !botOK {
			continue
		}
		for _, rp := range r.Principals {
			for _, p := range principals {
				if matchPattern(rp, p) {
					return true
				}
			}
		}
	}
	return false
}
