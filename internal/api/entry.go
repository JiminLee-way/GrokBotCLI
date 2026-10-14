package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Entry is one transcript entry. Known fields are decoded; Raw keeps the full JSON for --json output.
// Kinds: message, send-message, user-attachment, tool-call, mcp-app, notice, event, feedback, voice-call.
type Entry struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Seq         float64 `json:"seq,omitempty"`
	TimestampMs float64 `json:"timestampMs,omitempty"`
	Role        string  `json:"role,omitempty"`
	Content     string  `json:"content,omitempty"`
	Text        string  `json:"text,omitempty"`
	IsStreaming bool    `json:"isStreaming,omitempty"`
	Streaming   bool    `json:"streaming,omitempty"`
	Name        string  `json:"name,omitempty"`
	Status      string  `json:"status,omitempty"`
	Summary     string  `json:"summary,omitempty"`
	ClientNonce string  `json:"clientNonce,omitempty"`
	ReplyTo     string  `json:"replyTo,omitempty"`
	FileName    string  `json:"file_name,omitempty"`
	FilePath    string  `json:"file_path,omitempty"`
	Author      *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"author,omitempty"`
	FromAgent *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"fromAgent,omitempty"`
	ToAgent *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"toAgent,omitempty"`
	Message *SendMessage `json:"message,omitempty"`

	// card resolution state (send-message)
	RespondedValue  *string `json:"respondedValue,omitempty"`
	WidgetDismissed bool    `json:"widgetDismissed,omitempty"`
	WidgetSkipped   bool    `json:"widgetSkipped,omitempty"`
	FormResolution  string  `json:"formResolution,omitempty"`
	SecretProvided  bool    `json:"secretProvided,omitempty"`
	// files attached to a user message: [{url, byteSize}]
	Attachments []struct {
		URL      string  `json:"url"`
		ByteSize float64 `json:"byteSize,omitempty"`
	} `json:"attachments,omitempty"`

	Raw json.RawMessage `json:"-"`
}

type SendMessage struct {
	Type     string `json:"type"`
	Content  string `json:"content,omitempty"`
	Text     string `json:"text,omitempty"`
	Title    string `json:"title,omitempty"`
	Body     string `json:"body,omitempty"`
	URL      string `json:"url,omitempty"`
	Name     string `json:"name,omitempty"`
	FileName string `json:"file_name,omitempty"` // type "attachment"
	Images   []struct {
		URL string `json:"url"`
		Alt string `json:"alt,omitempty"`
	} `json:"images,omitempty"`
	Widget        *Widget        `json:"widget,omitempty"`
	FormRequest   *FormRequest   `json:"formRequest,omitempty"`
	SecretRequest *SecretRequest `json:"secretRequest,omitempty"`
	// drafts (email-draft / slack-draft / meeting-chat-draft): resent in full by sendDraft
	Draft json.RawMessage `json:"draft,omitempty"`
	// approval cards
	Approval *Card `json:"approval,omitempty"`
	Ask      *Card `json:"ask,omitempty"`
}

type Card struct {
	Status           string `json:"status"`
	RequestID        string `json:"requestId"`
	Reason           string `json:"reason,omitempty"`
	Command          string `json:"command,omitempty"`
	Summary          string `json:"summary,omitempty"`
	Action           string `json:"action,omitempty"`
	Description      string `json:"description,omitempty"`
	Target           string `json:"target,omitempty"`
	MachineID        string `json:"machineId,omitempty"`
	WorkingDirectory string `json:"workingDirectory,omitempty"`
	// connector-grant asks
	ServerID   string `json:"serverId,omitempty"`
	ServerName string `json:"serverName,omitempty"`
}

func (e *Entry) UnmarshalJSON(b []byte) error {
	type plain Entry
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*e = Entry(p)
	e.Raw = append(json.RawMessage(nil), b...)
	return nil
}

func (e Entry) MarshalJSON() ([]byte, error) {
	if len(e.Raw) > 0 {
		return e.Raw, nil
	}
	type plain Entry
	return json.Marshal(plain(e))
}

// Pending reports whether the entry is still being written.
func (e *Entry) Pending() bool {
	return e.IsStreaming || e.Streaming || (e.Kind == "tool-call" && e.Status == "pending")
}

// FromBot reports whether the entry was produced by a bot (not echoed user input).
func (e *Entry) FromBot() bool {
	switch e.Kind {
	case "message":
		return e.Role == "assistant" || e.FromAgent != nil
	case "user-attachment":
		return false
	}
	return true
}

// Speaker returns a short label of who produced the entry.
func (e *Entry) Speaker(botName string) string {
	switch {
	case e.Author != nil && e.Author.Name != "":
		return e.Author.Name
	case e.FromAgent != nil && e.FromAgent.Name != "":
		if e.ToAgent != nil && e.ToAgent.Name != "" {
			return e.FromAgent.Name + " → " + e.ToAgent.Name
		}
		return e.FromAgent.Name
	case e.ToAgent != nil && e.ToAgent.Name != "":
		return botName + " → " + e.ToAgent.Name
	case e.Kind == "message" && e.Role == "user", e.Kind == "user-attachment":
		return "you"
	case e.Kind == "tool-call":
		return "tool"
	case e.Kind == "notice", e.Kind == "event":
		return "·"
	}
	return botName
}

// Body renders the entry as plain text.
func (e *Entry) Body() string {
	switch e.Kind {
	case "message":
		return e.Content
	case "notice":
		return e.Text
	case "user-attachment":
		return "[attachment] " + firstNonEmpty(e.FileName, e.FilePath)
	case "tool-call":
		s := "⚙ " + strings.TrimSuffix(e.Name, "ToolCall") + " [" + e.Status + "]"
		if e.Summary != "" {
			s += " " + e.Summary
		}
		return s
	case "send-message":
		if e.Message == nil {
			return ""
		}
		m := e.Message
		switch m.Type {
		case "text":
			return m.Content
		case "attachment":
			return "[file] " + firstNonEmpty(m.FileName, m.Name, m.URL)
		case "widget":
			if m.Widget != nil {
				switch {
				case e.RespondedValue != nil:
					return "[question] " + m.Widget.Prompt + " → " + strings.ReplaceAll(*e.RespondedValue, "\n", ", ")
				case e.WidgetDismissed || e.WidgetSkipped:
					return "[question] " + m.Widget.Prompt + " → dismissed"
				}
				return "[question] " + m.Widget.Prompt + " (gb pending)"
			}
		case "user-form":
			if m.FormRequest != nil {
				return "[form] " + m.FormRequest.Title + " (gb pending)"
			}
		case "secret-request":
			if m.SecretRequest != nil {
				return "[secret request] " + m.SecretRequest.Label + " (gb pending)"
			}
		case "connector-grant":
			if m.Ask != nil {
				return "[app access] " + m.Ask.ServerName + " — " + m.Ask.Status + " (gb pending)"
			}
		case "auto-review-approval", "local-tool-permission":
			c := m.Approval
			if c == nil {
				c = m.Ask
			}
			if c != nil {
				return fmt.Sprintf("[approval %s] %s (gb approvals list)", c.Status, firstNonEmpty(c.Summary, c.Command, c.Action, c.Reason, c.Description))
			}
		}
		if t := firstNonEmpty(m.Content, m.Text, m.Title, m.Body); t != "" {
			return "[" + m.Type + "] " + t
		}
		return "[" + m.Type + "]"
	}
	return "[" + e.Kind + "]"
}

func (e *Entry) Time() string {
	if e.TimestampMs == 0 {
		return "     "
	}
	t := time.UnixMilli(int64(e.TimestampMs))
	if time.Since(t) > 20*time.Hour {
		return t.Format("01-02 15:04")
	}
	return t.Format("15:04")
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Approval is a pending approval card found in a transcript.
type Approval struct {
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	EntryID   string `json:"entryId"`
	RequestID string `json:"requestId"`
	Type      string `json:"type"`
	Card      *Card  `json:"details"`
}

func (e *Entry) PendingApproval() *Approval {
	if e.Kind != "send-message" || e.Message == nil {
		return nil
	}
	var c *Card
	switch e.Message.Type {
	case "auto-review-approval":
		c = e.Message.Approval
	case "local-tool-permission":
		c = e.Message.Ask
	}
	if c == nil || c.Status != "pending" || c.RequestID == "" {
		return nil
	}
	return &Approval{EntryID: e.ID, RequestID: c.RequestID, Type: e.Message.Type, Card: c}
}

// File is a file a bot (or user) attached to a transcript entry.
type File struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Path is the box path to read with readAttachmentChunk (empty for plain http(s) URLs).
	Path string  `json:"path,omitempty"`
	Size float64 `json:"size,omitempty"`
	Kind string  `json:"kind"` // image | file
}

// Files lists the files attached to this entry.
func (e *Entry) Files() []File {
	var out []File
	add := func(url, name, kind string, size float64) {
		if url == "" {
			return
		}
		f := File{Name: name, URL: url, Kind: kind, Size: size}
		if p, ok := strings.CutPrefix(url, "file://"); ok {
			f.Path = p
		} else if strings.HasPrefix(url, "/") {
			f.Path = url
		}
		if f.Name == "" {
			f.Name = url[strings.LastIndex(url, "/")+1:]
			if i := strings.IndexAny(f.Name, "?#"); i >= 0 {
				f.Name = f.Name[:i]
			}
		}
		out = append(out, f)
	}
	if m := e.Message; m != nil {
		if m.Type == "attachment" {
			add(m.URL, m.FileName, "file", 0)
		}
		for _, im := range m.Images {
			add(im.URL, "", "image", 0)
		}
	}
	for _, a := range e.Attachments {
		add(a.URL, "", "file", a.ByteSize)
	}
	if e.Kind == "user-attachment" {
		add(e.FilePath, e.FileName, "file", 0)
	}
	return out
}
