// gbdiscord — example Discord front-end for Grok Bot, built on the gbd daemon API.
//
//	Discord ──(@mention / DM / reply / slash)──▶ gbdiscord ──POST /v1/send──▶ gbd ──▶ Grok Bot
//	Discord ◀──(replies, typing, buttons)──────── gbdiscord ◀──GET /v1/events── gbd
//
// Every send goes out immediately (no queue): a second message to a bot that is still working steers its
// current turn, and different bots run in parallel. Replies are routed back with the `refs` gbd attaches.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

// ---------- config ----------

type Config struct {
	DiscordToken string            `json:"discordToken"` // or env DISCORD_TOKEN
	GBD          string            `json:"gbd"`          // "unix:///path/gbd.sock" or "http://127.0.0.1:8787"
	GBDToken     string            `json:"gbdToken"`     // for http (or env GBD_TOKEN)
	DefaultBot   string            `json:"defaultBot"`   // bot used for mentions/DMs when the channel has no mapping
	Channels     map[string]string `json:"channels"`     // Discord channel id -> Grok bot/group name

	ListenAll bool `json:"listenAll"` // in mapped channels, react to every message (needs Message Content intent)
	// MessageContent requests the privileged Message Content intent (enable it in the Developer Portal).
	// Without it Discord blanks text except in DMs and @mentions, so "reply to the bot" only works with it.
	MessageContent bool   `json:"messageContent"`
	GuildID        string `json:"guildId"` // register slash commands to this server (instant); empty = global (up to 1h)
	// MaxUploadMB overrides the per-server upload limit used for bot files (default: from the server's boost level).
	MaxUploadMB int `json:"maxUploadMB"`
	// AlertChannel receives Grok Bot alerts (usage limits, paused routines, failed syncs, ...).
	AlertChannel string `json:"alertChannel"`
	// Servers assigns Grok bots to Discord servers (guild id -> config). See personas.go.
	Servers map[string]ServerConfig `json:"servers"`
	// Personas posts replies under each bot's name via webhooks and creates @bot roles (default true).
	Personas *bool `json:"personas"`
	// Avatars maps bot name -> image URL for persona replies.
	Avatars map[string]string `json:"avatars"`
}

func loadConfig(path string) (*Config, error) {
	c := &Config{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if v := os.Getenv("DISCORD_TOKEN"); v != "" {
		c.DiscordToken = v
	}
	if v := os.Getenv("GBD_TOKEN"); v != "" {
		c.GBDToken = v
	}
	if v := os.Getenv("GBD_URL"); v != "" {
		c.GBD = v
	}
	if c.GBD == "" {
		home, _ := os.UserHomeDir()
		c.GBD = "unix://" + home + "/.config/gb/default/gbd.sock"
	}
	if c.DiscordToken == "" {
		return nil, errors.New("set DISCORD_TOKEN or discordToken in config")
	}
	return c, nil
}

// principals identify the Discord user to gbd, which enforces the per-bot ACL (gbd.json "acl").
// This bot never decides permissions itself, so every front-end shares one policy.
func principals(guildID, channelID string, u *discordgo.User, m *discordgo.Member) []string {
	p := []string{"discord:user:" + u.ID, "discord:channel:" + channelID}
	if guildID == "" {
		p = append(p, "discord:dm")
	} else {
		p = append(p, "discord:guild:"+guildID)
	}
	if m != nil {
		for _, r := range m.Roles {
			p = append(p, "discord:role:"+r)
		}
	}
	return p
}

var errForbidden = errors.New("forbidden")

// ---------- gbd client ----------

type GBD struct {
	base  string
	token string
	http  *http.Client // short requests
	sse   *http.Client // no timeout
}

func newGBD(addr, token string) *GBD {
	g := &GBD{base: addr, token: token}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if sock, ok := strings.CutPrefix(addr, "unix://"); ok {
		g.base = "http://gbd"
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}
	}
	g.http = &http.Client{Transport: tr, Timeout: 120 * time.Second}
	g.sse = &http.Client{Transport: tr}
	return g
}

func (g *GBD) req(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r, _ := http.NewRequestWithContext(ctx, method, g.base+path, rd)
	r.Header.Set("content-type", "application/json")
	if g.token != "" {
		r.Header.Set("authorization", "Bearer "+g.token)
	}
	res, err := g.http.Do(r)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.Unmarshal(raw, &e)
		if res.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: %s", errForbidden, e.Error)
		}
		return fmt.Errorf("gbd %s: %s", path, firstNonEmpty(e.Error, res.Status))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type Agent struct {
	ID, Name string
	IsGroup  bool
}

type Approval struct {
	AgentID   string         `json:"agentId"`
	EntryID   string         `json:"entryId"`
	RequestID string         `json:"requestId"`
	Type      string         `json:"type"`
	Details   map[string]any `json:"details"`
}

type Event struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	AgentID   string          `json:"agentId"`
	AgentName string          `json:"agentName"`
	EntryID   string          `json:"entryId"`
	Text      string          `json:"text"`
	Refs      []string        `json:"refs"`
	Running   *bool           `json:"running"`
	Approval  *Approval       `json:"approval"`
	Card      *Card           `json:"card"`
	Files     []File          `json:"files"`
	Alert     json.RawMessage `json:"alert"`
}

// events streams gbd events forever, resuming from the last seen id after a reconnect.
func (g *GBD) events(ctx context.Context, types string, on func(Event)) {
	var last int64
	for ctx.Err() == nil {
		path := fmt.Sprintf("/v1/events?types=%s&since=%d", types, last)
		r, _ := http.NewRequestWithContext(ctx, "GET", g.base+path, nil)
		if g.token != "" {
			r.Header.Set("authorization", "Bearer "+g.token)
		}
		res, err := g.sse.Do(r)
		if err == nil && res.StatusCode == 200 {
			log.Printf("connected to gbd events")
			sc := bufio.NewScanner(res.Body)
			sc.Buffer(make([]byte, 64<<10), 16<<20)
			for sc.Scan() {
				if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
					var e Event
					if json.Unmarshal([]byte(data), &e) == nil {
						last = e.ID
						go on(e) // events for different bots are handled in parallel
					}
				}
			}
			res.Body.Close()
		} else if err == nil {
			res.Body.Close()
			err = errors.New(res.Status)
		}
		log.Printf("gbd events disconnected (%v); retrying in 3s", err)
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

// ---------- bot ----------

type Bot struct {
	cfg *Config
	gbd *GBD
	dg  *discordgo.Session
	me  string

	mu      sync.Mutex
	actions map[string]action             // button key -> pending approval/draft
	typing  map[string]context.CancelFunc // agentId -> stop typing
	agents  []Agent
	p       *personas
}

func (b *Bot) ctx() context.Context { return context.Background() }

type draftAction struct{ AgentID, EntryID string }

type action struct {
	v  any // *Approval | *draftAction
	at time.Time
}

func main() {
	cfgPath := "config.json"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	dg, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		log.Fatal(err)
	}
	dg.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsGuildMessageReactions
	b := &Bot{cfg: cfg, gbd: newGBD(cfg.GBD, cfg.GBDToken), dg: dg, actions: map[string]action{}, typing: map[string]context.CancelFunc{}, p: newPersonas()}
	if cfg.ListenAll || cfg.MessageContent || (len(cfg.Servers) > 0 && b.personasOn()) {
		dg.Identify.Intents |= discordgo.IntentsMessageContent // @bot-role mentions need the message text
	}
	dg.AddHandler(func(_ *discordgo.Session, g *discordgo.GuildCreate) { go b.ensureRoles(g.ID) })
	dg.AddHandler(b.onMessage)
	dg.AddHandler(b.onInteraction)
	if err := dg.Open(); err != nil {
		log.Fatal(err)
	}
	defer dg.Close()
	b.me = dg.State.User.ID
	b.registerCommands()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go b.gbd.events(ctx, "message,activity,turn.done,approval,card,draft,alert", b.onEvent)
	log.Printf("gbdiscord running as %s", dg.State.User.Username)
	<-ctx.Done()
}

// ---------- Discord → Grok ----------

var botPrefix = regexp.MustCompile(`^\s*\[([^\]]{1,80})\]\s*`) // "[Bot Name] question" picks a bot explicitly

func (b *Bot) onMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot || m.WebhookID != "" {
		return // never react to bots, webhooks or ourselves: no loops
	}
	dm := m.GuildID == ""
	mentioned := false
	for _, u := range m.Mentions {
		mentioned = mentioned || u.ID == b.me
	}
	roleBots := b.mentionedBots(m.Message)
	replied, replyToBot := b.repliedTo(m.Message)
	replyToMe := replyToBot || (m.ReferencedMessage != nil && m.ReferencedMessage.Author != nil && m.ReferencedMessage.Author.ID == b.me)
	_, mapped := b.cfg.Channels[m.ChannelID]
	if !(dm || mentioned || len(roleBots) > 0 || replyToMe || (b.cfg.ListenAll && mapped)) {
		return
	}

	text := b.normalize(s, m.Message)
	// Who is addressed: @bot roles (several = all of them, in parallel), else the bot whose message is being
	// replied to, else "[Bot] ..." prefix, channel mapping, the server default, the global default.
	bots := roleBots
	if len(bots) == 0 && replyToBot {
		bots = []string{replied.AgentName}
	}
	if mt := botPrefix.FindStringSubmatch(text); mt != nil {
		bots, text = []string{mt[1]}, strings.TrimSpace(text[len(mt[0]):])
	}
	if len(bots) == 0 {
		bot := firstNonEmpty(b.cfg.Channels[m.ChannelID], b.cfg.Servers[m.GuildID].DefaultBot, b.cfg.DefaultBot)
		if bot == "" {
			return
		}
		bots = []string{bot}
	}
	if strings.TrimSpace(text) == "" && len(m.Attachments) == 0 {
		return
	}
	_ = s.MessageReactionAdd(m.ChannelID, m.ID, "👀")
	_ = s.ChannelTyping(m.ChannelID)
	var wg sync.WaitGroup
	for _, bot := range bots {
		if !b.allowedIn(m.GuildID, bot) {
			b.reply(m.ChannelID, m.ID, fmt.Sprintf("🚫 %s is not available in this server.", bot))
			continue
		}
		replyToID := ""
		if replyToBot && strings.EqualFold(bot, replied.AgentName) {
			bot, replyToID = replied.AgentID, replied.EntryID // continue exactly that message (sendPrompt.replyToId)
		}
		wg.Add(1)
		go func(bot, replyToID string) { // bots are independent: deliver in parallel
			defer wg.Done()
			b.deliver(s, m, bot, text, replyToID, dm || mentioned || len(roleBots) > 0 || replyToMe)
		}(bot, replyToID)
	}
	wg.Wait()
}

// deliver uploads the attachments for one bot and sends the prompt.
func (b *Bot) deliver(s *discordgo.Session, m *discordgo.MessageCreate, bot, text, replyToID string, direct bool) {
	who := principals(m.GuildID, m.ChannelID, m.Author, m.Member)
	attachments, skipped, err := b.uploadAttachments(context.Background(), bot, m.Attachments, who)
	if len(skipped) > 0 {
		b.reply(m.ChannelID, m.ID, "⚠️ Not sent (too large for Grok Bot): "+strings.Join(skipped, ", "))
	}
	if err == nil {
		if strings.TrimSpace(text) == "" && len(attachments) == 0 {
			_ = s.MessageReactionRemove(m.ChannelID, m.ID, "👀", "@me")
			return
		}
		req := map[string]any{
			"bot": bot, "text": b.header(s, m.Message, direct) + "\n" + text, "ref": m.ChannelID + "/" + m.ID, "attachments": attachments, "principals": who,
		}
		if replyToID != "" {
			req["replyToId"] = replyToID
		}
		err = b.gbd.req(context.Background(), "POST", "/v1/send", req, nil)
	}
	if err != nil {
		_ = s.MessageReactionRemove(m.ChannelID, m.ID, "👀", "@me")
		if errors.Is(err, errForbidden) {
			_ = s.MessageReactionAdd(m.ChannelID, m.ID, "🚫") // no permission for this bot: stay quiet
			return
		}
		b.reply(m.ChannelID, m.ID, "⚠️ Could not deliver: "+err.Error())
	}
}

// header carries Discord provenance inside the prompt (sendPrompt has no sender/channel fields).
func (b *Bot) header(s *discordgo.Session, m *discordgo.Message, direct bool) string {
	where := "dm"
	if m.GuildID != "" {
		ch := m.ChannelID
		if c, err := s.State.Channel(m.ChannelID); err == nil {
			ch = "#" + c.Name
		}
		where = "channel=" + ch
		if g, err := s.State.Guild(m.GuildID); err == nil {
			where = "server=" + g.Name + " " + where
		}
	}
	return fmt.Sprintf("[discord] %s from=@%s (id %s) directly_addressed=%v", where, m.Author.Username, m.Author.ID, direct)
}

var (
	userMention    = regexp.MustCompile(`<@!?(\d+)>`)
	channelMention = regexp.MustCompile(`<#(\d+)>`)
	roleMention    = regexp.MustCompile(`<@&(\d+)>`)
)

// normalize turns Discord mention syntax into readable names and drops the mention of this bot.
func (b *Bot) normalize(s *discordgo.Session, m *discordgo.Message) string {
	names := map[string]string{}
	for _, u := range m.Mentions {
		names[u.ID] = u.Username
	}
	t := userMention.ReplaceAllStringFunc(m.Content, func(x string) string {
		id := userMention.FindStringSubmatch(x)[1]
		if id == b.me {
			return ""
		}
		return "@" + firstNonEmpty(names[id], id)
	})
	t = channelMention.ReplaceAllStringFunc(t, func(x string) string {
		id := channelMention.FindStringSubmatch(x)[1]
		if c, err := s.State.Channel(id); err == nil {
			return "#" + c.Name
		}
		return x
	})
	t = roleMention.ReplaceAllStringFunc(t, func(x string) string {
		id := roleMention.FindStringSubmatch(x)[1]
		if bot, ok := b.botRoleName(m.GuildID, id); ok {
			return "@" + bot
		}
		if r, err := s.State.Role(m.GuildID, id); err == nil {
			return "@" + r.Name
		}
		return x
	})
	return strings.TrimSpace(t)
}

// ---------- Grok → Discord ----------

func parseRef(ref string) (channel, msg string, ok bool) {
	channel, msg, ok = strings.Cut(ref, "/")
	return
}

// targets returns one (channel, message-to-reply-to) per channel: the latest ref in each channel.
func (b *Bot) targets(e Event) [][2]string {
	latest := map[string]string{}
	var order []string
	for _, r := range e.Refs {
		if ch, msg, ok := parseRef(r); ok {
			if _, seen := latest[ch]; !seen {
				order = append(order, ch)
			}
			latest[ch] = msg
		}
	}
	if len(order) == 0 { // not started from Discord (e.g. the desktop app): mirror into a mapped channel
		for ch, bot := range b.cfg.Channels {
			if strings.EqualFold(bot, e.AgentName) || bot == e.AgentID {
				order, latest[ch] = append(order, ch), ""
			}
		}
	}
	out := make([][2]string, 0, len(order))
	for _, ch := range order {
		out = append(out, [2]string{ch, latest[ch]})
	}
	return out
}

func (b *Bot) onEvent(e Event) {
	switch e.Type {
	case "message":
		for _, t := range b.targets(e) {
			if len(e.Files) == 0 || !strings.HasPrefix(strings.TrimSpace(e.Text), "[file]") {
				b.sendAs(e, t[0], t[1], e.Text)
			}
			b.sendFiles(e, t[0], t[1])
		}
	case "approval", "card":
		b.renderCard(e)
	case "alert":
		b.postAlert(e)
	case "activity":
		if e.Running != nil && *e.Running {
			b.startTyping(e)
		} else {
			b.stopTyping(e.AgentID)
		}
	case "turn.done":
		b.stopTyping(e.AgentID)
		for _, r := range e.Refs {
			if ch, msg, ok := parseRef(r); ok {
				_ = b.dg.MessageReactionRemove(ch, msg, "👀", "@me")
				_ = b.dg.MessageReactionAdd(ch, msg, "✅")
			}
		}
	case "draft":
		key := b.remember(&draftAction{AgentID: e.AgentID, EntryID: e.EntryID})
		for _, t := range b.targets(e) {
			b.send(t[0], t[1], &discordgo.MessageSend{
				Embeds: []*discordgo.MessageEmbed{{Title: "✉️ " + e.AgentName + " drafted a message (review before it is sent)", Description: clip(e.Text, 3900), Color: 0x5865F2}},
				Components: buttons(
					discordgo.Button{Label: "Send", Style: discordgo.PrimaryButton, CustomID: "dr:send:" + key},
					discordgo.Button{Label: "Discard", Style: discordgo.SecondaryButton, CustomID: "dr:discard:" + key},
				),
			})
		}
	}
}

func buttons(bs ...discordgo.Button) []discordgo.MessageComponent {
	row := discordgo.ActionsRow{}
	for _, x := range bs {
		row.Components = append(row.Components, x)
	}
	return []discordgo.MessageComponent{row}
}

func (b *Bot) remember(v any) string {
	k := make([]byte, 6)
	_, _ = rand.Read(k)
	key := hex.EncodeToString(k)
	b.mu.Lock()
	b.actions[key] = action{v, time.Now()}
	for k, a := range b.actions { // buttons nobody clicks expire after a day
		if time.Since(a.at) > 24*time.Hour {
			delete(b.actions, k)
		}
	}
	b.mu.Unlock()
	return key
}

// startTyping shows "typing…" in every channel waiting on this bot until the turn ends (Discord's
// indicator lasts ~10s, so it is refreshed every 8s).
func (b *Bot) startTyping(e Event) {
	targets := b.targets(e)
	if len(targets) == 0 {
		return
	}
	b.mu.Lock()
	if _, on := b.typing[e.AgentID]; on {
		b.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	b.typing[e.AgentID] = cancel
	b.mu.Unlock()
	go func() {
		t := time.NewTicker(8 * time.Second)
		defer t.Stop()
		for {
			for _, x := range targets {
				_ = b.dg.ChannelTyping(x[0])
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (b *Bot) stopTyping(agentID string) {
	b.mu.Lock()
	if cancel := b.typing[agentID]; cancel != nil {
		cancel()
		delete(b.typing, agentID)
	}
	b.mu.Unlock()
}

// noPings: bot output never pings @everyone/roles/users (the reply itself still links to the message).
var noPings = &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}

func (b *Bot) reply(channel, replyTo, text string) (ids []string) {
	for i, part := range split(text, 2000) {
		ms := &discordgo.MessageSend{Content: part, AllowedMentions: noPings}
		if i == 0 && replyTo != "" {
			ms.Reference = &discordgo.MessageReference{MessageID: replyTo, ChannelID: channel, FailIfNotExists: new(bool)}
		}
		msg, err := b.dg.ChannelMessageSendComplex(channel, ms)
		if err != nil {
			log.Printf("send to %s: %v", channel, err)
			continue
		}
		ids = append(ids, msg.ID)
	}
	return ids
}

func (b *Bot) send(channel, replyTo string, ms *discordgo.MessageSend) {
	ms.AllowedMentions = noPings
	if replyTo != "" {
		ms.Reference = &discordgo.MessageReference{MessageID: replyTo, ChannelID: channel, FailIfNotExists: new(bool)}
	}
	if _, err := b.dg.ChannelMessageSendComplex(channel, ms); err != nil {
		log.Printf("send to %s: %v", channel, err)
	}
}

// split cuts text into chunks of at most max runes, preferring line breaks; a code fence that spans a
// cut is closed at the end of one chunk and re-opened at the start of the next.
func split(text string, max int) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out []string
	var cur []rune
	fence, body := "", false // open fence line; whether cur has content beyond the re-opened fence
	n := func(s string) int { return len([]rune(s)) }
	flush := func() {
		if body {
			chunk := string(cur)
			if fence != "" {
				chunk = strings.TrimRight(chunk, "\n") + "\n```"
			}
			out = append(out, chunk)
		}
		cur, body = nil, false
		if fence != "" {
			cur = []rune(fence + "\n")
		}
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		for n(line)+len(cur)+8 > max { // does not fit: cut
			room := max - len(cur) - 8
			if body && room < n(line) && n(line) <= max-n(fence)-8 {
				flush() // the line fits in a fresh chunk
				continue
			}
			r := []rune(line)
			cur, body = append(cur, r[:room]...), true
			line = string(r[room:])
			flush()
		}
		cur, body = append(cur, []rune(line)...), body || strings.TrimSpace(line) != ""
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "```") {
			if fence == "" {
				fence = t
			} else {
				fence = ""
			}
		}
	}
	fence = ""
	flush()
	return out
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---------- slash commands & buttons ----------

func (b *Bot) registerCommands() {
	botOpt := &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "bot", Description: "Grok bot or group (default: this channel's bot)", Autocomplete: true}
	cmds := []*discordgo.ApplicationCommand{
		{Name: "ask", Description: "Ask a Grok bot", Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "What to ask", Required: true}, botOpt}},
		{Name: "bots", Description: "List Grok bots"},
		{Name: "new", Description: "Start a fresh conversation (managers)", Options: []*discordgo.ApplicationCommandOption{botOpt}},
		{Name: "stop", Description: "Stop the running turn (managers)", Options: []*discordgo.ApplicationCommandOption{botOpt}},
		{Name: "undo", Description: "Roll back the last turn (managers)", Options: []*discordgo.ApplicationCommandOption{botOpt}},
	}
	if _, err := b.dg.ApplicationCommandBulkOverwrite(b.me, b.cfg.GuildID, cmds); err != nil {
		log.Printf("register slash commands: %v", err)
	}
}

func (b *Bot) respond(i *discordgo.InteractionCreate, text string, ephemeral bool) {
	data := &discordgo.InteractionResponseData{Content: text, AllowedMentions: noPings}
	if ephemeral {
		data.Flags = discordgo.MessageFlagsEphemeral
	}
	_ = b.dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: data})
}

func user(i *discordgo.InteractionCreate) *discordgo.User {
	if i.Member != nil {
		return i.Member.User
	}
	return i.User
}

func (b *Bot) onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	u := user(i)
	if u == nil {
		return
	}
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		d := i.ApplicationCommandData()
		opts := map[string]string{}
		for _, o := range d.Options {
			opts[o.Name] = o.StringValue()
		}
		bot := firstNonEmpty(opts["bot"], b.cfg.Channels[i.ChannelID], b.cfg.Servers[i.GuildID].DefaultBot, b.cfg.DefaultBot)
		if d.Name != "bots" && !b.allowedIn(i.GuildID, bot) {
			b.respond(i, "🚫 "+bot+" is not available in this server.", true)
			return
		}
		switch d.Name {
		case "bots":
			var list []Agent // gbd returns only the bots this user may talk to
			if err := b.gbd.req(context.Background(), "GET", "/v1/agents?principals="+url.QueryEscape(strings.Join(principals(i.GuildID, i.ChannelID, u, i.Member), ",")), nil, &list); err != nil {
				b.respond(i, "⚠️ "+err.Error(), true)
				return
			}
			lines := []string{}
			for _, a := range list {
				if !b.allowedIn(i.GuildID, a.Name) {
					continue // servers only show their own bots
				}
				kind := "🤖"
				if a.IsGroup {
					kind = "👥"
				}
				lines = append(lines, kind+" "+a.Name)
			}
			b.respond(i, strings.Join(lines, "\n"), true)
		case "ask":
			who := principals(i.GuildID, i.ChannelID, u, i.Member)
			var visible []Agent // check permission before posting anything public
			_ = b.gbd.req(context.Background(), "GET", "/v1/agents?principals="+url.QueryEscape(strings.Join(who, ",")), nil, &visible)
			ok := false
			for _, a := range visible {
				ok = ok || strings.EqualFold(a.Name, bot) || a.ID == bot || strings.HasSuffix(strings.ToLower(bot), ":"+strings.ToLower(a.Name))
			}
			if !ok {
				b.respond(i, "🚫 You are not allowed to use "+bot, true)
				return
			}
			b.respond(i, fmt.Sprintf("**%s** ← %s", bot, clip(opts["prompt"], 1800)), false)
			msg, err := s.InteractionResponse(i.Interaction)
			if err != nil {
				return
			}
			fake := &discordgo.Message{ID: msg.ID, ChannelID: i.ChannelID, GuildID: i.GuildID, Author: u}
			err = b.gbd.req(context.Background(), "POST", "/v1/send", map[string]any{
				"bot": bot, "text": b.header(s, fake, true) + "\n" + opts["prompt"], "ref": i.ChannelID + "/" + msg.ID,
				"principals": who,
			}, nil)
			if err != nil {
				b.reply(i.ChannelID, msg.ID, "⚠️ Could not deliver: "+err.Error())
			}
		case "new", "stop", "undo":
			err := b.gbd.req(context.Background(), "POST", "/v1/bots/"+d.Name, map[string]any{
				"bot": bot, "principals": principals(i.GuildID, i.ChannelID, u, i.Member)}, nil)
			if errors.Is(err, errForbidden) {
				b.respond(i, "🚫 You are not allowed to manage this bot.", true)
				return
			}
			if err != nil {
				b.respond(i, "⚠️ "+err.Error(), true)
				return
			}
			b.respond(i, fmt.Sprintf("✅ %s: /%s", bot, d.Name), false)
		}

	case discordgo.InteractionModalSubmit:
		b.onModal(s, i, u)

	case discordgo.InteractionApplicationCommandAutocomplete:
		b.autocompleteBots(i)

	case discordgo.InteractionMessageComponent:
		id := i.MessageComponentData().CustomID
		if strings.HasPrefix(id, "cd|") {
			b.onCardComponent(s, i, u)
			return
		}
		parts := strings.SplitN(id, ":", 3)
		if len(parts) != 3 {
			return
		}
		b.mu.Lock()
		act, found := b.actions[parts[2]]
		b.mu.Unlock()
		who := principals(i.GuildID, i.ChannelID, u, i.Member)
		var err error
		switch v := act.v.(type) {
		case *draftAction:
			err = b.gbd.req(context.Background(), "POST", "/v1/drafts/"+parts[1], map[string]any{"bot": v.AgentID, "entryId": v.EntryID, "principals": who}, nil)
		default:
			err = errors.New("this request expired or was already handled")
		}
		if errors.Is(err, errForbidden) && found {
			b.respond(i, "🚫 You are not allowed to approve for this bot.", true) // keep the buttons for someone who has it
			return
		}
		b.mu.Lock()
		delete(b.actions, parts[2])
		b.mu.Unlock()
		result := fmt.Sprintf("✅ %s — @%s", parts[1], u.Username)
		if err != nil {
			result = "⚠️ " + err.Error()
		}
		// Replace the buttons with the outcome so nobody clicks twice.
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{Content: result, Embeds: i.Message.Embeds, Components: []discordgo.MessageComponent{}, AllowedMentions: noPings},
		})
	}
}
