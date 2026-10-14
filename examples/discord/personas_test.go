package main

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestServerRoutingAndReplyMapping(t *testing.T) {
	b := &Bot{p: newPersonas(), cfg: &Config{Servers: map[string]ServerConfig{
		"guildA": {Bots: []string{"Alpha", "Beta"}},
		"guildB": {Bots: []string{"Gamma"}},
	}}}
	cases := []struct {
		guild, bot string
		want       bool
	}{
		{"guildA", "alpha", true}, {"guildA", "Gamma", false}, {"guildB", "Gamma", true}, {"guildC", "Alpha", false},
	}
	for _, c := range cases {
		if got := b.allowedIn(c.guild, c.bot); got != c.want {
			t.Errorf("allowedIn(%s,%s)=%v", c.guild, c.bot, got)
		}
	}
	if (&Bot{cfg: &Config{}}).serverBots("any") != nil {
		t.Error("without servers config every bot is allowed")
	}

	// Role mentions route to bots of that guild only.
	b.p.roles["guildA"] = map[string]string{"r1": "Alpha", "r2": "Beta"}
	got := b.mentionedBots(&discordgo.Message{GuildID: "guildA", MentionRoles: []string{"r2", "other", "r1"}})
	if len(got) != 2 || got[0] != "Beta" || got[1] != "Alpha" {
		t.Errorf("mentionedBots = %v", got)
	}

	// A Discord reply to a bot message maps back to its transcript entry (all split parts do).
	b.rememberSent("d1", sentMsg{"agent-1", "Alpha", "t0s1"})
	b.rememberSent("d2", sentMsg{"agent-1", "Alpha", "t0s1"})
	s, ok := b.repliedTo(&discordgo.Message{MessageReference: &discordgo.MessageReference{MessageID: "d2"}})
	if !ok || s.EntryID != "t0s1" || s.AgentID != "agent-1" {
		t.Errorf("repliedTo = %+v %v", s, ok)
	}
	if _, ok := b.repliedTo(&discordgo.Message{MessageReference: &discordgo.MessageReference{MessageID: "unknown"}}); ok {
		t.Error("unknown message must not map")
	}
}
