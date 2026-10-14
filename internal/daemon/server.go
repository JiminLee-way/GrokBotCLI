package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"gb/internal/api"
	"gb/internal/auth"
	"gb/internal/box"
)

// Config is gbd.json in the profile directory.
type Config struct {
	// Listen is an optional TCP address (e.g. "127.0.0.1:8787"). The Unix socket is always served.
	Listen string `json:"listen,omitempty"`
	// Token is required for TCP clients as "Authorization: Bearer <token>".
	Token string `json:"token,omitempty"`
	Hooks []Hook `json:"hooks,omitempty"`
	// ACL restricts what front-end users (principals) may do with which bots. Absent = allow all.
	ACL *ACL `json:"acl,omitempty"`
}

// forbidden is returned when the ACL denies a request (HTTP 403).
type forbidden struct{ msg string }

func (f forbidden) Error() string { return f.msg }

// principals: JSON body field, else the X-Gbd-Principals header (comma separated), else "local".
func principals(r *http.Request, body []string) []string {
	if len(body) > 0 {
		return body
	}
	if h := splitList(r.Header.Get("x-gbd-principals")); len(h) > 0 {
		return h
	}
	if q := splitList(r.URL.Query().Get("principals")); len(q) > 0 {
		return q
	}
	return []string{"local"}
}

func (s *Server) authorize(who []string, action string, ag *api.Agent) error {
	if s.Cfg.ACL.Allowed(who, action, ag, s.Hub.API.P.Name) {
		return nil
	}
	name := "this action"
	if ag != nil {
		name = ag.Name
	}
	return forbidden{fmt.Sprintf("not allowed: %s on %s for %s", action, name, strings.Join(who, ","))}
}

type Server struct {
	Hub *Hub
	Cfg Config
	Log *log.Logger
}

func (s *Server) Handler(requireToken bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/agents", s.agents)
	mux.HandleFunc("POST /v1/send", s.send)
	mux.HandleFunc("POST /v1/ask", s.ask)
	mux.HandleFunc("GET /v1/log", s.log)
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("POST /v1/approvals/resolve", s.resolveApproval)
	mux.HandleFunc("POST /v1/drafts/{action}", s.draft)
	mux.HandleFunc("POST /v1/call", s.call)
	mux.HandleFunc("POST /v1/bots/{action}", s.botAction)
	mux.HandleFunc("GET /v1/cards", s.cards)
	mux.HandleFunc("POST /v1/cards/respond", s.respondCard)
	mux.HandleFunc("POST /v1/upload", s.upload)
	mux.HandleFunc("GET /v1/files", s.files)
	mux.HandleFunc("POST /v1/rpc", s.rpc)
	mux.HandleFunc("GET /v1/alerts", s.alerts)
	mux.HandleFunc("POST /v1/alerts/dismiss", s.dismissAlert)
	if !requireToken {
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("authorization"), "Bearer ")
		if s.Cfg.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.Cfg.Token)) != 1 {
			writeErr(w, http.StatusUnauthorized, errors.New("missing or bad bearer token"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	out := map[string]any{"error": err.Error()}
	var fb forbidden
	if errors.As(err, &fb) {
		status = http.StatusForbidden
		out["forbidden"] = true
	}
	var ge *box.GatewayError
	if errors.As(err, &ge) {
		out["gateway"] = ge
		if ge.Status >= 400 && ge.Status < 500 {
			status = http.StatusBadGateway
		}
	}
	writeJSON(w, status, out)
}

func readBody(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "eventsConnected": s.Hub.Connected(), "agents": len(s.Hub.Roster())})
}

func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	list := s.Hub.Roster()
	if len(list) == 0 || r.URL.Query().Get("fresh") == "1" {
		var err error
		if list, err = s.Hub.API.Agents(r.Context(), true); err != nil {
			writeErr(w, 502, err)
			return
		}
		s.Hub.setRoster(list, true)
	}
	// Only the bots the caller may talk to.
	who := principals(r, nil)
	out := make([]api.Agent, 0, len(list))
	for i := range list {
		if s.authorize(who, "send", &list[i]) == nil {
			out = append(out, list[i])
		}
	}
	writeJSON(w, 200, out)
}

type sendReq struct {
	Bot       string `json:"bot"`  // id, name or unique prefix
	Text      string `json:"text"` // prompt
	Ref       string `json:"ref,omitempty"`
	ReplyToID string `json:"replyToId,omitempty"`
	Nonce     string `json:"nonce,omitempty"`
	Files     []struct {
		Name       string `json:"name"`
		DataBase64 string `json:"dataBase64"`
	} `json:"files,omitempty"`
	// Attachments are files already uploaded with POST /v1/upload (preferred for large files).
	Attachments []struct {
		Path string `json:"path"`
		Name string `json:"name"`
	} `json:"attachments,omitempty"`
	TimeoutSec int      `json:"timeoutSec,omitempty"` // ask only
	Principals []string `json:"principals,omitempty"` // who is asking (see ACL)
}

func (s *Server) doSend(ctx context.Context, r *http.Request, q *sendReq) (*api.Agent, *api.SendResult, error) {
	if strings.TrimSpace(q.Bot) == "" || strings.TrimSpace(q.Text) == "" {
		return nil, nil, errors.New("bot and text are required")
	}
	ag, err := s.resolve(ctx, q.Bot)
	if err != nil {
		return nil, nil, err
	}
	if err := s.authorize(principals(r, q.Principals), "send", ag); err != nil {
		return nil, nil, err
	}
	opts := api.SendOpts{ReplyToID: q.ReplyToID, ClientNonce: q.Nonce}
	if opts.ClientNonce == "" {
		opts.ClientNonce = auth.UUID()
	}
	for _, f := range q.Files {
		data, err := base64.StdEncoding.DecodeString(f.DataBase64)
		if err != nil {
			return nil, nil, fmt.Errorf("file %s: %w", f.Name, err)
		}
		path, err := s.Hub.API.Upload(ctx, ag.ID, f.Name, data)
		if err != nil {
			return nil, nil, err
		}
		opts.AttachmentPaths = append(opts.AttachmentPaths, path)
		opts.AttachmentNames = append(opts.AttachmentNames, f.Name)
	}
	for _, a := range q.Attachments {
		opts.AttachmentPaths = append(opts.AttachmentPaths, a.Path)
		opts.AttachmentNames = append(opts.AttachmentNames, firstNonEmpty(a.Name, a.Path))
	}
	// Register before sending so the echo that races back over the stream is attributed.
	s.Hub.NoteSend(ag.ID, opts.ClientNonce, q.Ref)
	res, err := s.Hub.API.Send(ctx, ag, q.Text, opts)
	if res != nil && (res.Delivery == "rejected" || res.Delivery == "not-received") {
		s.Hub.ForgetSend(ag.ID, opts.ClientNonce, q.Ref)
	}
	return ag, res, err
}

// send forwards immediately — no queue, no per-bot lock (Grok steers the running turn with mid-turn input).
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var q sendReq
	if err := readBody(r, &q); err != nil {
		writeErr(w, 400, err)
		return
	}
	_, res, err := s.doSend(r.Context(), r, &q)
	if err != nil {
		if res != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error(), "send": res})
			return
		}
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, res)
}

// ask sends and waits until the bot's turn finishes, returning the bot messages produced after the prompt.
// Overlapping asks to the same bot both receive the merged turn's replies (Grok answers them together).
func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	var q sendReq
	if err := readBody(r, &q); err != nil {
		writeErr(w, 400, err)
		return
	}
	timeout := 10 * time.Minute
	if q.TimeoutSec > 0 {
		timeout = time.Duration(q.TimeoutSec) * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	sub, ch, _ := s.Hub.Subscribe(0)
	defer s.Hub.Unsubscribe(sub)
	ag, res, err := s.doSend(ctx, r, &q)
	if err != nil {
		if res == nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 502, map[string]any{"error": err.Error(), "send": res})
		return
	}
	var msgs []Event
	sawBot := false
	idleCheck := time.NewTicker(3 * time.Second)
	defer idleCheck.Stop()
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			writeJSON(w, 504, map[string]any{"error": "timed out waiting for the reply", "send": res, "messages": msgs, "done": false})
			return
		case e, ok := <-ch:
			if !ok {
				writeJSON(w, 500, map[string]any{"error": "event stream subscriber dropped", "send": res, "messages": msgs})
				return
			}
			if e.AgentID != ag.ID {
				continue
			}
			switch e.Type {
			case "message", "approval", "draft", "widget":
				msgs, sawBot = append(msgs, e), true
			case "turn.done":
				if sawBot {
					writeJSON(w, 200, map[string]any{"send": res, "messages": msgs, "reply": joinText(msgs), "done": true})
					return
				}
			}
		case <-idleCheck.C:
			// Safety net if turn.done was missed (e.g. stream reconnect): idle + some reply = done.
			if sawBot || time.Since(start) > 90*time.Second {
				if a, err := s.Hub.API.Resolve(ctx, ag.ID); err == nil && !a.Busy() && (sawBot || time.Since(start) > 90*time.Second) {
					writeJSON(w, 200, map[string]any{"send": res, "messages": msgs, "reply": joinText(msgs), "done": true})
					return
				}
			}
		}
	}
}

func joinText(es []Event) string {
	var parts []string
	for _, e := range es {
		if e.Type == "message" && strings.TrimSpace(e.Text) != "" {
			parts = append(parts, e.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (s *Server) log(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ag, err := s.resolve(r.Context(), q.Get("bot"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if err := s.authorize(principals(r, nil), "read", ag); err != nil {
		writeErr(w, 403, err)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 30
	}
	before, _ := strconv.ParseFloat(q.Get("before"), 64)
	p, err := s.Hub.API.Tail(r.Context(), ag.ID, limit, before)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, p)
}

// events streams normalized events as SSE. Query: bots=a,b  types=message,turn.done  since=<event id>.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, errors.New("streaming unsupported"))
		return
	}
	q := r.URL.Query()
	bots := map[string]bool{}
	for _, b := range splitList(q.Get("bots")) {
		if a, err := s.Hub.API.Resolve(r.Context(), b); err == nil {
			bots[a.ID] = true
		} else {
			writeErr(w, 404, err)
			return
		}
	}
	types := map[string]bool{}
	for _, t := range splitList(q.Get("types")) {
		types[t] = true
	}
	since, _ := strconv.ParseInt(firstNonEmpty(q.Get("since"), r.Header.Get("Last-Event-ID")), 10, 64)
	id, ch, replay := s.Hub.Subscribe(since)
	defer s.Hub.Unsubscribe(id)
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.WriteHeader(200)
	write := func(e Event) {
		if (len(bots) > 0 && !bots[e.AgentID]) || (len(types) > 0 && !types[e.Type]) {
			return
		}
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, b)
	}
	for _, e := range replay {
		write(e)
	}
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	hb := time.NewTicker(20 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			write(e)
			fl.Flush()
		}
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func (s *Server) resolveApproval(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Bot, EntryID, RequestID, Decision string
		Principals                        []string
	}
	if err := readBody(r, &q); err != nil {
		writeErr(w, 400, err)
		return
	}
	ag, err := s.resolve(r.Context(), q.Bot)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if err := s.authorize(principals(r, q.Principals), "approve", ag); err != nil {
		writeErr(w, 403, err)
		return
	}
	if q.Decision != "approve" && q.Decision != "deny" {
		writeErr(w, 400, errors.New(`decision must be "approve" or "deny"`))
		return
	}
	p, err := s.Hub.API.Tail(r.Context(), ag.ID, 200, 0)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	var hit *api.Approval
	for i := range p.Entries {
		if ap := p.Entries[i].PendingApproval(); ap != nil && ap.EntryID == q.EntryID && ap.RequestID == q.RequestID {
			hit = ap
		}
	}
	if hit == nil {
		writeErr(w, 409, errors.New("no matching pending approval (already resolved or stale)"))
		return
	}
	accept := q.Decision == "approve"
	args := map[string]any{"agentId": ag.ID, "entryId": hit.EntryID, "requestId": hit.RequestID}
	method := "resolveAutoReviewApproval"
	if hit.Type == "local-tool-permission" {
		method, args["resolution"] = "resolveLocalToolPermission", map[bool]string{true: "allow-once", false: "deny"}[accept]
	} else {
		args["resolution"] = map[bool]string{true: "approved", false: "denied"}[accept]
	}
	if err := s.Hub.API.Call(r.Context(), method, args, nil); err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "agentId": ag.ID, "entryId": hit.EntryID, "decision": q.Decision})
}

// draft handles POST /v1/drafts/send|discard {bot, entryId, draft?}. send without draft resends the original.
func (s *Server) draft(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Bot        string          `json:"bot"`
		EntryID    string          `json:"entryId"`
		Draft      json.RawMessage `json:"draft,omitempty"`
		Principals []string        `json:"principals,omitempty"`
	}
	if err := readBody(r, &q); err != nil {
		writeErr(w, 400, err)
		return
	}
	ag, err := s.resolve(r.Context(), q.Bot)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if err := s.authorize(principals(r, q.Principals), "approve", ag); err != nil {
		writeErr(w, 403, err)
		return
	}
	var out json.RawMessage
	switch r.PathValue("action") {
	case "discard":
		err = s.Hub.API.Call(r.Context(), "discardDraft", map[string]any{"agentId": ag.ID, "entryId": q.EntryID}, &out)
	case "send":
		payload := q.Draft
		if len(payload) == 0 {
			p, terr := s.Hub.API.Tail(r.Context(), ag.ID, 200, 0)
			if terr != nil {
				writeErr(w, 502, terr)
				return
			}
			for _, en := range p.Entries {
				if en.ID == q.EntryID && en.Message != nil && strings.HasSuffix(en.Message.Type, "-draft") {
					payload, _ = json.Marshal(map[string]any{"type": en.Message.Type, "draft": en.Message.Draft})
				}
			}
			if len(payload) == 0 {
				writeErr(w, 404, errors.New("draft entry not found in the last 200 entries"))
				return
			}
		}
		err = s.Hub.API.Call(r.Context(), "sendDraft", map[string]any{"agentId": ag.ID, "entryId": q.EntryID, "draft": payload}, &out)
	default:
		writeErr(w, 404, errors.New("action must be send or discard"))
		return
	}
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "result": out})
}

func (s *Server) call(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Method     string          `json:"method"`
		Args       json.RawMessage `json:"args"`
		Principals []string        `json:"principals,omitempty"`
	}
	if err := readBody(r, &q); err != nil || q.Method == "" {
		writeErr(w, 400, errors.New(`body must be {"method": "...", "args": {...}}`))
		return
	}
	if err := s.authorize(principals(r, q.Principals), "admin", nil); err != nil {
		writeErr(w, 403, err)
		return
	}
	var args any = map[string]any{}
	if len(q.Args) > 0 {
		args = q.Args
	}
	var out json.RawMessage
	if err := s.Hub.API.Call(r.Context(), q.Method, args, &out); err != nil {
		writeErr(w, 502, err)
		return
	}
	if len(out) == 0 {
		out = json.RawMessage("null")
	}
	w.Header().Set("content-type", "application/json")
	_, _ = w.Write(out)
}

// Serve runs the Unix socket (always) and the optional TCP listener until ctx ends.
func (s *Server) Serve(ctx context.Context, sock string) error {
	_ = os.Remove(sock)
	ul, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o600)
	servers := []*http.Server{{Handler: s.Handler(false), ReadHeaderTimeout: 10 * time.Second}}
	listeners := []net.Listener{ul}
	if s.Cfg.Listen != "" {
		if s.Cfg.Token == "" {
			return errors.New(`gbd.json "listen" requires a "token"`)
		}
		tl, err := net.Listen("tcp", s.Cfg.Listen)
		if err != nil {
			return err
		}
		servers = append(servers, &http.Server{Handler: s.Handler(true), ReadHeaderTimeout: 10 * time.Second})
		listeners = append(listeners, tl)
		s.Log.Printf("listening on tcp %s (bearer token required)", s.Cfg.Listen)
	}
	s.Log.Printf("listening on unix %s", sock)
	errc := make(chan error, len(servers))
	for i := range servers {
		go func(i int) { errc <- servers[i].Serve(listeners[i]) }(i)
	}
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(sctx)
	}
	_ = os.Remove(sock)
	return err
}

// resolve accepts "account:name" for this daemon's own account as well as plain names/ids.
func (s *Server) resolve(ctx context.Context, ref string) (*api.Agent, error) {
	if acct, rest, ok := strings.Cut(ref, ":"); ok && acct == s.Hub.API.P.Name {
		ref = rest
	}
	return s.Hub.API.Resolve(ctx, ref)
}

// botAction handles POST /v1/bots/new|stop|undo {bot, principals} (admin).
func (s *Server) botAction(w http.ResponseWriter, r *http.Request) {
	method := map[string]string{"new": "startFreshConversation", "stop": "interruptAgentRun", "undo": "rollBackConversation"}[r.PathValue("action")]
	if method == "" {
		writeErr(w, 404, errors.New("action must be new, stop or undo"))
		return
	}
	var q struct {
		Bot        string   `json:"bot"`
		Principals []string `json:"principals,omitempty"`
	}
	if err := readBody(r, &q); err != nil {
		writeErr(w, 400, err)
		return
	}
	ag, err := s.resolve(r.Context(), q.Bot)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if err := s.authorize(principals(r, q.Principals), "admin", ag); err != nil {
		writeErr(w, 403, err)
		return
	}
	if err := s.Hub.API.Call(r.Context(), method, map[string]any{"id": ag.ID}, nil); err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "agentId": ag.ID, "action": r.PathValue("action")})
}

// cards lists pending cards for ?bot= (read).
func (s *Server) cards(w http.ResponseWriter, r *http.Request) {
	ag, err := s.resolve(r.Context(), r.URL.Query().Get("bot"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if err := s.authorize(principals(r, nil), "read", ag); err != nil {
		writeErr(w, 403, err)
		return
	}
	cs, err := s.Hub.API.Cards(r.Context(), ag)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, cs)
}

// respondCard answers any pending card (approve permission):
// {bot, entryId, action, values?, custom?, fields?, value?, command?, principals?}
func (s *Server) respondCard(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Bot        string   `json:"bot"`
		EntryID    string   `json:"entryId"`
		Principals []string `json:"principals,omitempty"`
		api.CardResponse
	}
	if err := readBody(r, &q); err != nil {
		writeErr(w, 400, err)
		return
	}
	ag, err := s.resolve(r.Context(), q.Bot)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if err := s.authorize(principals(r, q.Principals), "approve", ag); err != nil {
		writeErr(w, 403, err)
		return
	}
	c, err := s.Hub.API.FindCard(r.Context(), ag, q.EntryID)
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	if err := s.Hub.API.Respond(r.Context(), ag, c, q.CardResponse); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "agentId": ag.ID, "entryId": c.EntryID, "kind": c.Kind, "action": q.Action})
}

// upload streams the raw request body to the box: POST /v1/upload?bot=&filename= (Content-Length required).
// Limits follow the app: 25 MiB, 200 MiB for video. Returns {path, name, size} for /v1/send "attachments".
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ag, err := s.resolve(r.Context(), q.Get("bot"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if err := s.authorize(principals(r, nil), "send", ag); err != nil {
		writeErr(w, 403, err)
		return
	}
	name := q.Get("filename")
	if name == "" || r.ContentLength <= 0 {
		writeErr(w, 400, errors.New("filename and a Content-Length are required"))
		return
	}
	if limit := api.UploadLimit(name); r.ContentLength > limit {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": (&api.TooLarge{Name: name, Size: r.ContentLength, Limit: limit}).Error(), "limit": limit, "size": r.ContentLength})
		return
	}
	path, err := s.Hub.API.UploadStream(r.Context(), ag.ID, name, r.Body, r.ContentLength)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"path": path, "name": name, "size": r.ContentLength})
}

// files streams a bot file: GET /v1/files?bot=&path=|url=&max=<bytes>.
// Headers X-File-Name / X-File-Size / Content-Type; 413 JSON when the file is larger than max.
func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ag, err := s.resolve(r.Context(), q.Get("bot"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if err := s.authorize(principals(r, nil), "read", ag); err != nil {
		writeErr(w, 403, err)
		return
	}
	f := api.File{Path: q.Get("path"), URL: q.Get("url")}
	if p, ok := strings.CutPrefix(f.URL, "file://"); ok {
		f.Path, f.URL = p, ""
	}
	if f.Path == "" && f.URL == "" {
		writeErr(w, 400, errors.New("path or url is required"))
		return
	}
	f.Name = firstNonEmpty(q.Get("name"), lastSegment(firstNonEmpty(f.Path, f.URL)))
	max, _ := strconv.ParseInt(q.Get("max"), 10, 64)
	started := false
	info, err := s.Hub.API.ReadFile(r.Context(), ag.ID, f, w, max, func(i api.FileInfo) {
		started = true
		w.Header().Set("x-file-name", i.Name)
		w.Header().Set("x-file-size", strconv.FormatInt(i.Size, 10))
		w.Header().Set("content-type", firstNonEmpty(i.Mime, "application/octet-stream"))
		if i.Size > 0 {
			w.Header().Set("content-length", strconv.FormatInt(i.Size, 10))
		}
		w.WriteHeader(200)
	})
	if err != nil && !started {
		var tl *api.TooLarge
		if errors.As(err, &tl) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": err.Error(), "name": info.Name, "size": tl.Size, "limit": tl.Limit})
			return
		}
		writeErr(w, 502, err)
	}
}

func lastSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// rpc proxies a Cursor backend Connect RPC (admin): {rpc: "GrokBotService/ListGrokBotAgents", args, principals}.
func (s *Server) rpc(w http.ResponseWriter, r *http.Request) {
	var q struct {
		RPC        string          `json:"rpc"`
		Args       json.RawMessage `json:"args"`
		Principals []string        `json:"principals,omitempty"`
	}
	if err := readBody(r, &q); err != nil || !strings.Contains(q.RPC, "/") {
		writeErr(w, 400, errors.New(`body must be {"rpc": "Service/Method", "args": {...}}`))
		return
	}
	if err := s.authorize(principals(r, q.Principals), "admin", nil); err != nil {
		writeErr(w, 403, err)
		return
	}
	rpc := q.RPC
	if !strings.HasPrefix(rpc, "aiserver.") {
		rpc = "aiserver.v1." + rpc
	}
	var args any = map[string]any{}
	if len(q.Args) > 0 {
		args = q.Args
	}
	var out json.RawMessage
	if err := box.Backend(r.Context(), s.Hub.API.P, rpc, args, &out); err != nil {
		writeErr(w, 502, err)
		return
	}
	w.Header().Set("content-type", "application/json")
	_, _ = w.Write(out)
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(principals(r, nil), "read", nil); err != nil {
		writeErr(w, 403, err)
		return
	}
	var out json.RawMessage
	if err := s.Hub.API.Call(r.Context(), "getTrays", nil, &out); err != nil {
		writeErr(w, 502, err)
		return
	}
	w.Header().Set("content-type", "application/json")
	_, _ = w.Write(out)
}

func (s *Server) dismissAlert(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID         string   `json:"id"`
		Principals []string `json:"principals,omitempty"`
	}
	if err := readBody(r, &q); err != nil || q.ID == "" {
		writeErr(w, 400, errors.New(`body must be {"id": "..."}`))
		return
	}
	if err := s.authorize(principals(r, q.Principals), "admin", nil); err != nil {
		writeErr(w, 403, err)
		return
	}
	if err := s.Hub.API.Call(r.Context(), "dismissTray", map[string]any{"id": q.ID}, nil); err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
