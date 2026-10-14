package daemon

import (
	"testing"

	"gb/internal/api"
)

func TestACL(t *testing.T) {
	xm := &api.Agent{ID: "id-r", Name: "Researcher"}
	nova := &api.Agent{ID: "id-w", Name: "Writer"}
	acl := &ACL{Rules: []Rule{
		{Principals: []string{"local"}, Bots: []string{"*"}, Actions: []string{"*"}},
		{Principals: []string{"discord:role:111"}, Bots: []string{"Researcher"}, Actions: []string{"send", "read"}},
		{Principals: []string{"discord:user:222"}, Bots: []string{"work:*"}, Actions: []string{"send", "approve", "admin"}},
		{Principals: []string{"discord:channel:333"}, Bots: []string{"id-w"}, Actions: []string{"send"}},
	}}
	if err := acl.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		who    []string
		action string
		bot    *api.Agent
		acct   string
		want   bool
	}{
		{[]string{"local"}, "admin", nil, "work", true},
		{[]string{"discord:user:9", "discord:role:111"}, "send", xm, "work", true},
		{[]string{"discord:user:9", "discord:role:111"}, "send", nova, "work", false},
		{[]string{"discord:user:9", "discord:role:111"}, "approve", xm, "work", false},
		{[]string{"discord:user:222"}, "admin", nova, "work", true},
		{[]string{"discord:user:222"}, "admin", nova, "personal", false},
		{[]string{"discord:user:222"}, "admin", nil, "work", false}, // raw call needs bots:"*"
		{[]string{"discord:user:5", "discord:channel:333"}, "send", nova, "work", true},
		{[]string{"discord:user:5"}, "send", xm, "work", false},
		{nil, "send", xm, "work", false},
	}
	for i, c := range cases {
		if got := acl.Allowed(c.who, c.action, c.bot, c.acct); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
	if !(*ACL)(nil).Allowed([]string{"x"}, "admin", nil, "") {
		t.Error("no ACL must allow everything")
	}
	if (&ACL{Rules: []Rule{{Principals: []string{"a"}, Bots: []string{"*"}, Actions: []string{"fly"}}}}).Validate() == nil {
		t.Error("unknown action must fail validation")
	}
}
