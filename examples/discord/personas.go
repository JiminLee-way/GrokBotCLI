package main

// One Discord app fronting many Grok bots ("personas"):
//
//   - servers: each Discord server gets its own set of bots ("servers": {"<guildId>": {"bots": [...]}}).
//   - @mention: every bot gets a mentionable role with its name in its servers, so typing @Researcher
//     autocompletes. Mentioning several bot roles sends the message to each of them, in parallel.
//     (Nobody holds these roles, so a mention pings nobody.)
//   - replies are posted through a channel webhook under the bot's own name (and avatar, if configured).
//   - replying (Discord "Reply") to a bot's message continues that exact message: the Discord message is
//     mapped back to its transcript entry and sent as sendPrompt.replyToId — Grok then answers about
//     that message specifically, even when the bot split its answer into several messages.
//
// Needs the bot permissions Manage Roles and Manage Webhooks, and the Message Content intent
// (role mentions are not "mentions of the bot", so Discord would otherwise blank the text).

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
)

type ServerConfig struct {
	Bots         []string `json:"bots"`         // Grok bots/groups available in this server
	DefaultBot   string   `json:"defaultBot"`   // for @app mentions without a bot role / prefix
	AlertChannel string   `json:"alertChannel"` // per-server alert channel (falls back to the global one)
}

const webhookName = "Grok Bots"

// sentMsg links a Discord message to the transcript entry it shows.
type sentMsg struct{ AgentID, AgentName, EntryID string }

type personas struct {
	mu    sync.Mutex
	roles map[string]map[string]string  // guildID -> roleID -> bot name
	hooks map[string]*discordgo.Webhook // parent channelID -> our webhook
	sent  map[string]sentMsg            // Discord message id -> entry
	order []string                      // insertion order, to bound sent
}

func newPersonas() *personas {
	return &personas{roles: map[string]map[string]string{}, hooks: map[string]*discordgo.Webhook{}, sent: map[string]sentMsg{}}
}

func (b *Bot) personasOn() bool { return b.cfg.Personas == nil || *b.cfg.Personas }

// serverBots returns the bots allowed in a guild; nil means "no per-server restriction".
func (b *Bot) serverBots(guildID string) []string {
	if sc, ok := b.cfg.Servers[guildID]; ok {
		return sc.Bots
	}
	if len(b.cfg.Servers) > 0 && guildID != "" {
		return []string{} // servers are configured and this one is not: no bots here
	}
	return nil
}

func (b *Bot) allowedIn(guildID, bot string) bool {
	list := b.serverBots(guildID)
	if list == nil {
		return true
	}
	for _, x := range list {
		if strings.EqualFold(x, bot) {
			return true
		}
	}
	return false
}

// ensureRoles creates a mentionable role per bot (named after it) in a configured server.
func (b *Bot) ensureRoles(guildID string) {
	sc, ok := b.cfg.Servers[guildID]
	if !ok || !b.personasOn() {
		return
	}
	existing, err := b.dg.GuildRoles(guildID)
	if err != nil {
		log.Printf("guild %s: list roles: %v", guildID, err)
		return
	}
	m := map[string]string{}
	for _, bot := range sc.Bots {
		var id string
		for _, r := range existing {
			if strings.EqualFold(r.Name, bot) {
				id = r.ID
			}
		}
		if id == "" {
			yes, no, none := true, false, int64(0)
			r, err := b.dg.GuildRoleCreate(guildID, &discordgo.RoleParams{Name: bot, Mentionable: &yes, Hoist: &no, Permissions: &none})
			if err != nil {
				log.Printf("guild %s: create role %q (needs Manage Roles): %v", guildID, bot, err)
				continue
			}
			id = r.ID
			log.Printf("guild %s: created role @%s", guildID, bot)
		}
		m[id] = bot
	}
	b.p.mu.Lock()
	b.p.roles[guildID] = m
	b.p.mu.Unlock()
}

// mentionedBots returns the bots whose roles the message mentions.
func (b *Bot) mentionedBots(m *discordgo.Message) []string {
	b.p.mu.Lock()
	defer b.p.mu.Unlock()
	var out []string
	for _, rid := range m.MentionRoles {
		if bot, ok := b.p.roles[m.GuildID][rid]; ok {
			out = append(out, bot)
		}
	}
	return out
}

// botRoleIDs lets normalize() drop bot-role mentions from the prompt text.
func (b *Bot) botRoleName(guildID, roleID string) (string, bool) {
	b.p.mu.Lock()
	defer b.p.mu.Unlock()
	bot, ok := b.p.roles[guildID][roleID]
	return bot, ok
}

func (b *Bot) rememberSent(msgID string, s sentMsg) {
	if msgID == "" || s.EntryID == "" {
		return
	}
	b.p.mu.Lock()
	defer b.p.mu.Unlock()
	b.p.sent[msgID] = s
	b.p.order = append(b.p.order, msgID)
	if len(b.p.order) > 20000 {
		for _, old := range b.p.order[:10000] {
			delete(b.p.sent, old)
		}
		b.p.order = append([]string(nil), b.p.order[10000:]...)
	}
}

// repliedTo returns the transcript entry behind the Discord message being replied to.
func (b *Bot) repliedTo(m *discordgo.Message) (sentMsg, bool) {
	if m.MessageReference == nil {
		return sentMsg{}, false
	}
	b.p.mu.Lock()
	defer b.p.mu.Unlock()
	s, ok := b.p.sent[m.MessageReference.MessageID]
	return s, ok
}

// webhook returns our webhook for a channel (threads use their parent's webhook).
func (b *Bot) webhook(channelID string) (*discordgo.Webhook, string, error) {
	parent, thread := channelID, ""
	if ch, err := b.dg.State.Channel(channelID); err == nil && ch.IsThread() {
		parent, thread = ch.ParentID, channelID
	}
	b.p.mu.Lock()
	w := b.p.hooks[parent]
	b.p.mu.Unlock()
	if w != nil {
		return w, thread, nil
	}
	hooks, err := b.dg.ChannelWebhooks(parent)
	if err != nil {
		return nil, "", err
	}
	for _, h := range hooks {
		if h.Name == webhookName && h.Token != "" && (h.ApplicationID == b.me || (h.User != nil && h.User.ID == b.me)) {
			w = h
		}
	}
	if w == nil {
		if w, err = b.dg.WebhookCreate(parent, webhookName, ""); err != nil {
			return nil, "", err
		}
	}
	b.p.mu.Lock()
	b.p.hooks[parent] = w
	b.p.mu.Unlock()
	return w, thread, nil
}

// sendAs posts text under the bot's own name. The first part carries a jump link to the message it answers,
// since webhook messages cannot be Discord replies. Falls back to the app's own reply if webhooks fail.
func (b *Bot) sendAs(e Event, channel, replyTo, text string) {
	parts := split(text, 1900)
	if len(parts) == 0 {
		return
	}
	ch, err := b.dg.State.Channel(channel)
	if !b.personasOn() || err != nil || ch.GuildID == "" {
		b.replyRecorded(e, channel, replyTo, text)
		return
	}
	w, thread, err := b.webhook(channel)
	if err != nil {
		log.Printf("webhook in %s (needs Manage Webhooks): %v — replying as the app", channel, err)
		b.replyRecorded(e, channel, replyTo, text)
		return
	}
	if replyTo != "" {
		parts[0] = fmt.Sprintf("-# ↪ https://discord.com/channels/%s/%s/%s\n%s", ch.GuildID, channel, replyTo, parts[0])
	}
	for _, part := range parts {
		msg, err := b.dg.WebhookThreadExecute(w.ID, w.Token, true, thread, &discordgo.WebhookParams{
			Content: part, Username: clip(e.AgentName, 80), AvatarURL: b.cfg.Avatars[e.AgentName], AllowedMentions: noPings,
		})
		if err != nil {
			log.Printf("webhook send: %v — replying as the app", err)
			b.p.mu.Lock()
			delete(b.p.hooks, w.ChannelID) // deleted webhook? recreate next time
			b.p.mu.Unlock()
			b.replyRecorded(e, channel, replyTo, part)
			continue
		}
		if msg != nil {
			b.rememberSent(msg.ID, sentMsg{e.AgentID, e.AgentName, e.EntryID})
		}
	}
}

// replyRecorded is reply() that also remembers which entry each Discord message shows.
func (b *Bot) replyRecorded(e Event, channel, replyTo, text string) {
	for _, id := range b.reply(channel, replyTo, text) {
		b.rememberSent(id, sentMsg{e.AgentID, e.AgentName, e.EntryID})
	}
}

// autocompleteBots answers /ask's bot option with the bots of this server.
func (b *Bot) autocompleteBots(i *discordgo.InteractionCreate) {
	typed := ""
	for _, o := range i.ApplicationCommandData().Options {
		if o.Focused {
			typed = strings.ToLower(o.StringValue())
		}
	}
	list := b.serverBots(i.GuildID)
	if list == nil {
		var all []Agent
		_ = b.gbd.req(b.ctx(), "GET", "/v1/agents", nil, &all)
		for _, a := range all {
			list = append(list, a.Name)
		}
	}
	var choices []*discordgo.ApplicationCommandOptionChoice
	for _, n := range list {
		if typed == "" || strings.Contains(strings.ToLower(n), typed) {
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: clip(n, 100), Value: n})
		}
		if len(choices) == 25 {
			break
		}
	}
	_ = b.dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{Choices: choices},
	})
}
