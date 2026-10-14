package main

// Files in both directions and interactive cards (questions, forms, secret requests, app access, approvals).
//
// Size limits (checked before anything is transferred):
//   Discord → Grok: Grok Bot accepts 25 MiB per file (200 MiB for video). Discord itself lets people upload
//                   10 MiB on a free account and up to 500 MiB with Nitro, so Grok's limit is the one that bites.
//   Grok → Discord: a bot may upload what the server allows: 10 MiB by default, 50 MiB at boost level 2,
//                   100 MiB at level 3 (override with "maxUploadMB"). Larger files are announced instead,
//                   with the command to fetch them (`gb files get`).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

const (
	grokMaxBytes      = 25 << 20
	grokMaxVideoBytes = 200 << 20
)

var videoExt = map[string]bool{".mp4": true, ".mov": true, ".m4v": true, ".webm": true, ".mkv": true, ".avi": true, ".mpeg": true, ".mpg": true}

func grokLimit(name string) int64 {
	if videoExt[strings.ToLower(filepath.Ext(name))] {
		return grokMaxVideoBytes
	}
	return grokMaxBytes
}

func mib(n int64) string { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }

// File is a file a bot attached (from gbd message events).
type File struct {
	Name string  `json:"name"`
	URL  string  `json:"url"`
	Path string  `json:"path"`
	Size float64 `json:"size"`
	Kind string  `json:"kind"`
}

// Card mirrors gbd's pending card (only the fields the UI needs).
type Card struct {
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	EntryID   string `json:"entryId"`
	Kind      string `json:"kind"`
	Type      string `json:"type"`
	Approval  *struct {
		Command, Action, Summary, Reason, Target, WorkingDirectory string
	} `json:"approval"`
	Widget *struct {
		Prompt      string `json:"prompt"`
		HelpText    string `json:"helpText"`
		MultiSelect bool   `json:"multiSelect"`
		AllowCustom bool   `json:"allowCustom"`
		Options     []struct {
			Label       string `json:"label"`
			Value       string `json:"value"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"widget"`
	Form *struct {
		Title       string `json:"title"`
		Instruction string `json:"instruction"`
		Fields      []struct {
			ID, Label, Type, Placeholder string
			Required, Secret             bool
		} `json:"fields"`
	} `json:"form"`
	Secret *struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"secret"`
	Grant *struct {
		ServerName string `json:"serverName"`
	} `json:"grant"`
}

// ---------- gbd file endpoints ----------

func (g *GBD) upload(ctx context.Context, bot, name string, size int64, body io.Reader, who []string) (string, error) {
	u := g.base + "/v1/upload?bot=" + url.QueryEscape(bot) + "&filename=" + url.QueryEscape(name)
	req, _ := http.NewRequestWithContext(ctx, "POST", u, body)
	req.ContentLength = size
	req.Header.Set("x-gbd-principals", strings.Join(who, ","))
	if g.token != "" {
		req.Header.Set("authorization", "Bearer "+g.token)
	}
	res, err := g.sse.Do(req) // no timeout: large files
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		Path  string `json:"path"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("%w: %s", errForbidden, out.Error)
	}
	if res.StatusCode != 200 {
		return "", fmt.Errorf("upload %s: %s", name, firstNonEmpty(out.Error, res.Status))
	}
	return out.Path, nil
}

// file opens a bot file; a *tooLarge error means it exceeds max.
func (g *GBD) file(ctx context.Context, bot string, f File, max int64) (*http.Response, error) {
	q := url.Values{"bot": {bot}, "name": {f.Name}, "max": {strconv.FormatInt(max, 10)}}
	if f.Path != "" {
		q.Set("path", f.Path)
	} else {
		q.Set("url", f.URL)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", g.base+"/v1/files?"+q.Encode(), nil)
	if g.token != "" {
		req.Header.Set("authorization", "Bearer "+g.token)
	}
	res, err := g.sse.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusRequestEntityTooLarge {
		var tl struct {
			Size int64 `json:"size"`
		}
		_ = json.NewDecoder(res.Body).Decode(&tl)
		res.Body.Close()
		return nil, &tooLarge{size: tl.Size, limit: max}
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, fmt.Errorf("download %s: %s", f.Name, res.Status)
	}
	return res, nil
}

type tooLarge struct{ size, limit int64 }

func (t *tooLarge) Error() string {
	return fmt.Sprintf("%s is over the %s limit", mib(t.size), mib(t.limit))
}

// ---------- Discord → Grok ----------

// uploadAttachments streams Discord attachments to the bot. Files over Grok's limit are skipped and reported.
func (b *Bot) uploadAttachments(ctx context.Context, bot string, atts []*discordgo.MessageAttachment, who []string) (paths []map[string]string, skipped []string, err error) {
	for _, a := range atts {
		if limit := grokLimit(a.Filename); int64(a.Size) > limit {
			skipped = append(skipped, fmt.Sprintf("%s (%s > Grok Bot limit %s)", a.Filename, mib(int64(a.Size)), mib(limit)))
			continue
		}
		res, err := http.Get(a.URL)
		if err != nil {
			return nil, nil, err
		}
		path, err := b.gbd.upload(ctx, bot, a.Filename, int64(a.Size), res.Body, who)
		res.Body.Close()
		if err != nil {
			return nil, nil, err
		}
		paths = append(paths, map[string]string{"path": path, "name": a.Filename})
	}
	return paths, skipped, nil
}

// ---------- Grok → Discord ----------

// discordLimit is the largest file this bot may upload to a channel.
func (b *Bot) discordLimit(channelID string) int64 {
	if b.cfg.MaxUploadMB > 0 {
		return int64(b.cfg.MaxUploadMB) << 20
	}
	ch, err := b.dg.State.Channel(channelID)
	if err != nil || ch.GuildID == "" {
		return 10 << 20
	}
	g, err := b.dg.State.Guild(ch.GuildID)
	if err != nil {
		return 10 << 20
	}
	switch g.PremiumTier {
	case discordgo.PremiumTier2:
		return 50 << 20
	case discordgo.PremiumTier3:
		return 100 << 20
	}
	return 10 << 20
}

// sendFiles posts each file the bot attached, or a note when it is too large for the channel.
func (b *Bot) sendFiles(e Event, channel, replyTo string) {
	for _, f := range e.Files {
		limit := b.discordLimit(channel)
		res, err := b.gbd.file(context.Background(), e.AgentID, f, limit)
		var tl *tooLarge
		switch {
		case errors.As(err, &tl):
			b.reply(channel, replyTo, fmt.Sprintf("📎 **%s** is %s — larger than this server's %s upload limit.\nFetch it on the server: `gb files get %s %s`",
				f.Name, mib(tl.size), mib(limit), e.AgentName, firstNonEmpty(f.Path, f.URL)))
			continue
		case err != nil:
			log.Printf("file %s: %v", f.Name, err)
			continue
		}
		name := firstNonEmpty(f.Name, res.Header.Get("x-file-name"), "file")
		_, err = b.dg.ChannelMessageSendComplex(channel, &discordgo.MessageSend{
			Files:           []*discordgo.File{{Name: name, ContentType: res.Header.Get("content-type"), Reader: res.Body}},
			AllowedMentions: noPings,
		})
		res.Body.Close()
		if err != nil { // e.g. 40005 Request entity too large: the limit was guessed wrong
			b.reply(channel, replyTo, fmt.Sprintf("📎 Could not upload **%s** here (%v). Fetch it on the server: `gb files get %s %s`", name, err, e.AgentName, firstNonEmpty(f.Path, f.URL)))
		}
	}
}

// ---------- cards ----------

func (b *Bot) renderCard(e Event) {
	c := e.Card
	if c == nil {
		return
	}
	key := b.remember(c)
	id := func(action, arg string) string { return "cd|" + key + "|" + action + "|" + arg }
	var embed discordgo.MessageEmbed
	var rows []discordgo.MessageComponent
	addRow := func(bs ...discordgo.MessageComponent) { rows = append(rows, discordgo.ActionsRow{Components: bs}) }

	switch c.Kind {
	case "approval":
		ap := c.Approval
		desc := []string{}
		for _, kv := range [][2]string{{"Command", ap.Command}, {"Action", ap.Action}, {"Summary", ap.Summary}, {"Reason", ap.Reason}, {"Target", ap.Target}, {"Directory", ap.WorkingDirectory}} {
			if kv[1] != "" {
				desc = append(desc, fmt.Sprintf("**%s**: %s", kv[0], clip(kv[1], 900)))
			}
		}
		embed = discordgo.MessageEmbed{Title: "🔐 " + c.AgentName + " needs approval", Description: strings.Join(desc, "\n"), Color: 0xF5A623}
		addRow(
			discordgo.Button{Label: "Approve once", Style: discordgo.SuccessButton, CustomID: id("approve", "")},
			discordgo.Button{Label: "Always allow", Style: discordgo.PrimaryButton, CustomID: id("always", "")},
			discordgo.Button{Label: "Deny", Style: discordgo.DangerButton, CustomID: id("deny", "")},
		)
	case "widget":
		w := c.Widget
		embed = discordgo.MessageEmbed{Title: "❓ " + c.AgentName, Description: clip(w.Prompt+"\n"+w.HelpText, 3900), Color: 0x5865F2}
		if w.MultiSelect || len(w.Options) > 20 {
			opts := []discordgo.SelectMenuOption{}
			for i, o := range w.Options {
				if i == 25 {
					break
				}
				opts = append(opts, discordgo.SelectMenuOption{Label: clip(o.Label, 100), Value: strconv.Itoa(i + 1), Description: clip(o.Description, 100)})
			}
			max := 1
			if w.MultiSelect {
				max = len(opts)
			}
			min := 1
			addRow(discordgo.SelectMenu{CustomID: id("select", ""), Placeholder: "Choose…", MinValues: &min, MaxValues: max, Options: opts})
		} else {
			var row []discordgo.MessageComponent
			for i, o := range w.Options {
				if len(row) == 5 {
					addRow(row...)
					row = nil
				}
				row = append(row, discordgo.Button{Label: clip(o.Label, 80), Style: discordgo.SecondaryButton, CustomID: id("opt", strconv.Itoa(i+1))})
			}
			if len(row) > 0 {
				addRow(row...)
			}
		}
		extra := []discordgo.MessageComponent{discordgo.Button{Label: "Dismiss", Style: discordgo.SecondaryButton, CustomID: id("dismiss", "")}}
		if w.AllowCustom {
			extra = append([]discordgo.MessageComponent{discordgo.Button{Label: "Other…", Style: discordgo.PrimaryButton, CustomID: id("custom", "")}}, extra...)
		}
		addRow(extra...)
	case "form":
		f := c.Form
		lines := []string{clip(f.Instruction, 1000)}
		for _, x := range f.Fields {
			lines = append(lines, "• "+x.Label)
		}
		embed = discordgo.MessageEmbed{Title: "📝 " + c.AgentName + ": " + f.Title, Description: clip(strings.Join(lines, "\n"), 3900), Color: 0x5865F2}
		if len(f.Fields) <= 5 {
			addRow(discordgo.Button{Label: "Fill in", Style: discordgo.PrimaryButton, CustomID: id("fill", "")},
				discordgo.Button{Label: "Dismiss", Style: discordgo.SecondaryButton, CustomID: id("dismiss", "")})
		} else {
			embed.Footer = &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Too many fields for Discord; on the server run: gb form %s %s id=value ...", c.AgentName, c.EntryID)}
		}
	case "secret":
		s := c.Secret
		embed = discordgo.MessageEmbed{Title: "🔑 " + c.AgentName + " asks for: " + s.Label, Description: clip(s.Description, 2000) +
			"\n\n_The value goes through Discord to Grok Bot. For sensitive keys prefer `gb secret` on the server._", Color: 0xED4245}
		addRow(discordgo.Button{Label: "Enter value", Style: discordgo.DangerButton, CustomID: id("secret", "")})
	case "grant":
		embed = discordgo.MessageEmbed{Title: "🔌 " + c.AgentName + " wants to use " + c.Grant.ServerName, Color: 0x57F287}
		addRow(
			discordgo.Button{Label: "Allow once", Style: discordgo.SuccessButton, CustomID: id("allow", "")},
			discordgo.Button{Label: "Always (this bot)", Style: discordgo.PrimaryButton, CustomID: id("allow-bot", "")},
			discordgo.Button{Label: "Always (all bots)", Style: discordgo.PrimaryButton, CustomID: id("allow-team", "")},
			discordgo.Button{Label: "Skip", Style: discordgo.SecondaryButton, CustomID: id("skip", "")},
		)
	default:
		return
	}
	if len(rows) > 5 {
		rows = rows[:5]
	}
	for _, t := range b.targets(e) {
		b.send(t[0], t[1], &discordgo.MessageSend{Embeds: []*discordgo.MessageEmbed{&embed}, Components: rows})
	}
}

// onCardComponent handles buttons/selects with ids "cd|<key>|<action>|<arg>".
func (b *Bot) onCardComponent(s *discordgo.Session, i *discordgo.InteractionCreate, u *discordgo.User) {
	parts := strings.SplitN(i.MessageComponentData().CustomID, "|", 4)
	if len(parts) != 4 {
		return
	}
	key, action, arg := parts[1], parts[2], parts[3]
	b.mu.Lock()
	act, ok := b.actions[key]
	b.mu.Unlock()
	c, _ := act.v.(*Card)
	if !ok || c == nil {
		b.respond(i, "This request expired or was already handled.", true)
		return
	}
	resp := map[string]any{"bot": c.AgentID, "entryId": c.EntryID}
	switch action {
	case "opt":
		resp["action"], resp["values"] = "answer", []string{arg}
	case "select":
		resp["action"], resp["values"] = "answer", i.MessageComponentData().Values
	case "custom", "fill", "secret": // these need text: open a modal
		b.openModal(i, key, action, c)
		return
	default: // approve | always | deny | dismiss | allow | allow-bot | allow-team | skip
		resp["action"] = action
	}
	b.finishCard(i, key, c, resp, u, true)
}

func (b *Bot) openModal(i *discordgo.InteractionCreate, key, action string, c *Card) {
	var inputs []discordgo.MessageComponent
	title := c.AgentName
	text := func(id, label, placeholder string, required bool, style discordgo.TextInputStyle) {
		inputs = append(inputs, discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.TextInput{
			CustomID: id, Label: clip(label, 45), Style: style, Required: required, Placeholder: clip(placeholder, 100), MaxLength: 4000,
		}}})
	}
	switch action {
	case "custom":
		title = "Your answer"
		text("custom", clip(c.Widget.Prompt, 45), "", true, discordgo.TextInputParagraph)
	case "fill":
		title = c.Form.Title
		for _, f := range c.Form.Fields {
			text(f.ID, f.Label, f.Placeholder, f.Required, discordgo.TextInputShort)
		}
	case "secret":
		title = c.Secret.Label
		text("value", c.Secret.Label, "", true, discordgo.TextInputShort)
	}
	_ = b.dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{CustomID: "md|" + key + "|" + action, Title: clip(title, 45), Components: inputs},
	})
}

// onModal handles modal submissions with ids "md|<key>|<action>".
func (b *Bot) onModal(s *discordgo.Session, i *discordgo.InteractionCreate, u *discordgo.User) {
	d := i.ModalSubmitData()
	parts := strings.SplitN(d.CustomID, "|", 3)
	if len(parts) != 3 {
		return
	}
	key, action := parts[1], parts[2]
	b.mu.Lock()
	act, ok := b.actions[key]
	b.mu.Unlock()
	c, _ := act.v.(*Card)
	if !ok || c == nil {
		b.respond(i, "This request expired or was already handled.", true)
		return
	}
	values := map[string]string{}
	for _, row := range d.Components {
		if r, ok := row.(*discordgo.ActionsRow); ok {
			for _, comp := range r.Components {
				if t, ok := comp.(*discordgo.TextInput); ok {
					values[t.CustomID] = t.Value
				}
			}
		}
	}
	resp := map[string]any{"bot": c.AgentID, "entryId": c.EntryID}
	switch action {
	case "custom":
		resp["action"], resp["custom"] = "answer", values["custom"]
	case "fill":
		resp["action"], resp["fields"] = "submit", values
	case "secret":
		resp["action"], resp["value"] = "submit", values["value"]
	}
	b.finishCard(i, key, c, resp, u, false)
}

// finishCard sends the answer to gbd (which enforces the "approve" permission) and closes the card.
func (b *Bot) finishCard(i *discordgo.InteractionCreate, key string, c *Card, resp map[string]any, u *discordgo.User, fromComponent bool) {
	resp["principals"] = principals(i.GuildID, i.ChannelID, u, i.Member)
	err := b.gbd.req(context.Background(), "POST", "/v1/cards/respond", resp, nil)
	if errors.Is(err, errForbidden) {
		b.respond(i, "🚫 You are not allowed to answer for this bot.", true) // leave it for someone who is
		return
	}
	b.mu.Lock()
	delete(b.actions, key)
	b.mu.Unlock()
	result := fmt.Sprintf("✅ %s — @%s", resp["action"], u.Username)
	if err != nil {
		result = "⚠️ " + err.Error()
	}
	if !fromComponent || i.Message == nil {
		b.respond(i, result, false)
		return
	}
	_ = b.dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Content: result, Embeds: i.Message.Embeds, Components: []discordgo.MessageComponent{}, AllowedMentions: noPings},
	})
}

// ---------- alerts ----------

func (b *Bot) postAlert(e Event) {
	// Servers that host this bot get it in their own alert channel; everything else goes to the global one.
	channels := map[string]bool{}
	for gid, sc := range b.cfg.Servers {
		if sc.AlertChannel != "" && (e.AgentName == "" || b.allowedIn(gid, e.AgentName)) {
			channels[sc.AlertChannel] = true
		}
	}
	if len(channels) == 0 && b.cfg.AlertChannel != "" {
		channels[b.cfg.AlertChannel] = true
	}
	if len(channels) == 0 {
		return
	}
	var a struct {
		ErrorKind string  `json:"errorKind"`
		Title     string  `json:"title"`
		Detail    string  `json:"detail"`
		Count     float64 `json:"count"`
	}
	_ = json.Unmarshal(e.Alert, &a)
	title := "⚠️ " + firstNonEmpty(a.Title, strings.ReplaceAll(a.ErrorKind, "_", " "), "Grok Bot alert")
	if e.AgentName != "" {
		title += " — " + e.AgentName
	}
	for ch := range channels {
		b.send(ch, "", &discordgo.MessageSend{Embeds: []*discordgo.MessageEmbed{{Title: clip(title, 250), Description: clip(a.Detail, 3900), Color: 0xED4245}}})
	}
}
