<p align="center">
  <img src="assets/logo.png" alt="GrokBotCLI" width="180">
</p>

<h1 align="center">GrokBotCLI</h1>

<p align="center">
  A headless Grok Bot client for Linux servers: one static binary, no desktop app required.<br>
  Manage bots, chat, automate, and bridge your bots into Discord.
</p>

---

> **Unofficial.** This project is not affiliated with, endorsed by, or supported by xAI, SpaceX, Anysphere/Cursor,
> or Discord. "Grok Bot" and other names belong to their owners. It talks to the same service endpoints the
> official desktop client uses, for interoperability with your own account. Use it at your own risk and in line
> with the service's terms.
>
> **Takedown / concerns:** if you are a rights holder or believe anything here is a problem, email
> **leeway@mail.grokbot.com** and it will be removed immediately.

> **Status (v0.1.0):** the CLI and the daemon are verified against a live account (sign-in, bots, groups, chat,
> replies, cards, files, automations, apps, events, webhooks, ACL). The Discord bridge compiles and passes its unit
> tests but has **not yet been run against a real Discord server** — please report issues.

## Contents

- [What it does](#what-it-does)
- [Install](#install)
- [Quick start](#quick-start)
- [Command reference](#command-reference)
- [Multiple accounts](#multiple-accounts)
- [The daemon (gbd)](#the-daemon-gbd)
- [Discord bridge](#discord-bridge)
- [File size limits](#file-size-limits)
- [Architecture](#architecture)
- [Security notes](#security-notes)
- [Known limitations](#known-limitations)
- [Building from source](#building-from-source)

## What it does

| Area | Highlights |
|---|---|
| **Sign-in** | Own login (works over SSH: open the printed URL anywhere), automatic token refresh, logout, multiple accounts |
| **Bots & groups** | Create / rename / update / duplicate / delete bots; group chats (auto-named), members, people from your team |
| **Chat** | `send`, `ask` (waits for the full answer), history with paging, threads, replies to a specific message, live `watch`, search |
| **Concurrency** | No queue: a message to a busy bot joins its running turn immediately; different bots always run in parallel |
| **Interactive cards** | Answer approvals (once/always/deny, edited command), questions (single/multi-select, custom), forms, secret requests, app-access grants |
| **Files** | Send files to bots (streamed, size-checked); list / download files bots produce; search media |
| **Automations** | Cron and webhook routines: create, update, enable/disable, run, delete, webhook URL |
| **Apps & integrations** | Browse the app catalog, connect apps (OAuth works headless), tools on/off, per-app instructions, listeners (GitHub/GitLab/Slack/...), box secrets |
| **Operations** | Box status / wake, health doctor, alerts (usage limits, paused routines), skills library, raw gateway and backend calls |
| **Daemon (gbd)** | One long-lived connection, local HTTP API, normalized event stream, signed webhooks, per-bot permissions (ACL), systemd unit |
| **Discord bridge** | One Discord app for many bots: per-server bot sets, `@Bot` role mentions, replies under each bot's name, reply-to-message continuity, buttons/menus/forms for cards, files both ways |

## Install

Download a binary for your platform from the [Releases](../../releases/latest) page:

| File | Platform |
|---|---|
| `gb-linux-amd64`, `gb-linux-arm64` | CLI + daemon for Linux servers |
| `gb-darwin-arm64`, `gb-darwin-amd64` | CLI + daemon for macOS |
| `gbdiscord-linux-amd64`, `gbdiscord-linux-arm64`, `gbdiscord-darwin-arm64`, `gbdiscord-darwin-amd64` | Discord bridge (optional) |

```sh
chmod +x gb-linux-amd64 && sudo mv gb-linux-amd64 /usr/local/bin/gb
gb version
```

Checksums are in `SHA256SUMS`. The binaries are statically linked and have no runtime dependencies.

## Quick start

```sh
gb login            # prints a URL: open it in any browser (laptop, phone), approve, done
gb doctor           # credentials, box, gateway health, event stream
gb bots list
gb ask Researcher "Summarize today's AI news in three bullets"
```

Login never needs a local callback port, so it works the same on a headless server over SSH. Tokens are stored in
`~/.config/gb/<account>/credentials.json` (mode 0600) and refreshed automatically before they expire.
Alternatives: `gb login --api-key <key>` or copy `credentials.json` from another machine.

## Command reference

Bots and groups can be referenced by id, exact name, or a unique name prefix. Every command accepts `--json`.

### Account

```sh
gb login [--api-key KEY]        gb logout        gb whoami        gb doctor
gb accounts list [--check] | add <name> | remove <name> | default [name]
gb box status | wake
```

### Bots and groups

```sh
gb bots list [--all-accounts] | get <bot> | dup <bot> | delete <bot>...
gb bots create <name> -d "instructions" [--title T] [--avatar-shape S] [--avatar-color C]
gb bots update <bot> [--name N] [--title T] [-d TEXT] [--notify on|off] [--hidden on|off] [--drafting on|off]

gb groups create [name] --member A --member B [--person EMAIL]   # no name = auto-named like the app
gb groups update <group> --name N     gb groups members <group>     gb groups delete <group>
gb groups add|remove|set <group> <bot>...
gb groups people <group> | invite <group> <person> | kick <group> <person> | leave <group>
gb people                                                         # team members you can invite
```

Group chats with people need a Cursor team account; a group then holds up to 3 bots and 20 people.

### Chat

```sh
gb send <bot> "message" [--file a.png] [--reply-to ENTRY_ID] [--nonce N]
gb ask  <bot> "question" [--timeout 10m] [--show-tools]    # waits until the bot is done
gb log  <bot> [-n 30] [--before SEQ] [--after ENTRY_ID]
gb thread <bot> <rootEntryId>
gb watch [bot...] [--approvals]                            # live
gb search "keyword"          gb stop <bot>          gb reset <bot>          gb undo <bot>
```

`--reply-to` continues a specific message: if a bot answered in three parts, replying to the second part makes the
bot respond about that part.

### Cards (things a bot waits on you for)

```sh
gb pending [bot] [--all]
gb approvals approve|deny <bot> <entry> [--always|--never] [--command "edited command"]
gb answer <bot> <entry> 2                      # option number, label or value
gb answer <bot> <entry> Red Blue               # multi-select
gb answer <bot> <entry> --custom "text" | --dismiss
gb form   <bot> <entry> email=me@example.com password=env:PW | --dismiss
gb secret <bot> <entry> [env:VAR]              # reads stdin by default, so it stays out of shell history
gb grant  <bot> <entry> allow|allow-bot|allow-team|skip
```

### Files

```sh
gb files list <bot> [-n 50]
gb files get  <bot> <path> [-o FILE|-]
gb files search "invoice"
```

### Automations

```sh
gb auto list [bot]
gb auto add <bot> --name morning --prompt "Brief me" --cron "30 8 * * 1-5" [--exact] [--off]
gb auto add <bot> --name inbox --prompt "Process the payload" --webhook
gb auto update <bot> <name|id> [--name N] [--prompt P] [--cron EXPR]
gb auto on|off|rm <bot> <name|id>
gb auto run <bot> <name|id> [--send]           # --send: post the routine's prompt directly
gb auto webhook <bot> <name|id>                # URL (and key) for webhook routines
```

Cron uses the standard five fields (`minute hour day month weekday`); the service reports it back in words, e.g.
"Weekdays at 8:30 AM".

### Apps, integrations, secrets

```sh
gb apps list | catalog [query] [-n 30]
gb apps connect <app> [--set KEY=VALUE|KEY=env:VAR] [--account NAME] [--reauth] [--port 8787]
gb apps disconnect <app> [--account NAME] [--all]
gb apps tools <app> [--disable TOOL] [--enable TOOL]
gb apps instructions <app> ["text" | --clear]
gb listeners list | connect <github|gitlab|bitbucket|azure-devops|origin|slack> | disconnect <platform>
gb secrets list | set KEY=VALUE | unset KEY
```

Connecting an app on a server: `gb apps connect` prints the sign-in URL and finishes from whichever happens first:
a listener on `localhost:8787` (same machine, or `ssh -L 8787:localhost:8787 server`), you pasting the browser's
final `localhost` URL into the terminal, or the app turning connected on its own.

`setBoxSecrets` replaces the whole set and existing values cannot be read back, so `gb secrets set` refuses to
overwrite secrets it did not set itself unless you pass `--force`.

### Operations and raw access

```sh
gb alerts [--clear] | alerts dismiss <id>
gb skills list | add <SKILL.md|dir|url> | remove <id|name>
gb events [--channel NAME]                       # raw event stream (debugging)
gb call <method> '{json}'                        # any box gateway method, see docs/GATEWAY_METHODS.md
gb rpc <Service>/<Method> '{json}'               # any backend RPC
```

## Multiple accounts

```sh
gb accounts add work          # sign in as another account
gb accounts add personal
gb accounts list --check      # all accounts: user, token expiry, box state, bot count (checked in parallel)
gb bots list -A               # bots of every account, with an ACCOUNT column
gb ask work:"Researcher" "..."          # account:bot
gb ask "Researcher" "..."               # default account first, then a unique match in the others
```

Each account has its own box, plan and skill library; a group cannot mix bots from different accounts. Removing an
account revokes its server session, unless another local account is signed in as the same user.

## The daemon (gbd)

`gbd` keeps one connection to your box and serves a local API, a normalized event stream and webhooks. It is what
a chat bridge, a script or a dashboard should talk to.

```sh
gb daemon run                      # foreground
gb daemon install [--system]       # writes a systemd unit (prints the enable command and loginctl enable-linger)
gb daemon status
```

The API is served on a Unix socket, `~/.config/gb/<account>/gbd.sock` (mode 0600), and optionally on TCP with a
bearer token. Configuration lives in `~/.config/gb/<account>/gbd.json`:

```json
{
  "listen": "127.0.0.1:8787",
  "token": "change-me",
  "hooks": [
    {"url": "https://example.com/grok-hook", "secret": "s3cret", "events": ["message", "turn.done", "card", "alert"], "bots": ["Researcher"]}
  ],
  "acl": {"rules": [
    {"principals": ["local"], "bots": ["*"], "actions": ["*"]},
    {"principals": ["discord:role:111111111111111111"], "bots": ["Researcher", "Writer"], "actions": ["send", "read"]},
    {"principals": ["discord:user:222222222222222222"], "bots": ["*"], "actions": ["send", "read", "approve", "admin"]}
  ]}
}
```

### API

| Request | Purpose |
|---|---|
| `GET /v1/health` | status |
| `GET /v1/agents` | bots and groups (filtered to what the caller may use) |
| `POST /v1/send {bot, text, ref?, replyToId?, nonce?, attachments?}` | send now (no queue) |
| `POST /v1/ask {..., timeoutSec?}` | send and wait for the finished turn → `{reply, messages}` |
| `GET /v1/log?bot=&limit=&before=` | history |
| `GET /v1/events?bots=&types=&since=` | normalized events (SSE); `since` replays anything missed |
| `GET /v1/cards?bot=` · `POST /v1/cards/respond {bot, entryId, action, values?, custom?, fields?, value?, command?}` | pending cards and answers |
| `POST /v1/upload?bot=&filename=` (raw body) | streamed upload → `{path}` for `attachments: [{path, name}]` |
| `GET /v1/files?bot=&path=&max=` | streamed download (413 when larger than `max`) |
| `POST /v1/drafts/send\|discard`, `POST /v1/bots/new\|stop\|undo` | drafts and bot actions |
| `GET /v1/alerts` · `POST /v1/alerts/dismiss` | alerts |
| `POST /v1/call {method, args}` · `POST /v1/rpc {rpc, args}` | raw gateway / backend calls (admin) |

Add `principals: [...]` (or the `X-Gbd-Principals` header) to act on behalf of a person; the ACL then applies.

### Events (SSE and webhooks)

| type | When | Key fields |
|---|---|---|
| `prompt` | input reached a bot | `text`, `ref`, `nonce` |
| `message` | a bot posted a message | `text`, `refs`, `entryId`, `files[]`, `entry` |
| `activity` | a bot started/stopped working or typing | `running`, `composing`, `refs` |
| `turn.done` | a turn finished (after its last message) | `refs` |
| `approval` / `card` | something waits for an answer | `card{kind, approval\|widget\|form\|secret\|grant}` |
| `draft` | an outgoing email/Slack draft waits for review | `entryId`, `text` |
| `alert` | usage limit, paused routines, failed sync, ... | `alert` |
| `agent.updated` / `agent.deleted` | roster changes | `agent` |

`refs` are the `ref` values of every prompt in the turn. Input that arrives mid-turn is merged into the running
turn, so one answer can reply to several prompts; route it back to all of them.

Webhook requests carry `X-Gbd-Event`, `X-Gbd-Delivery` (dedupe on it), `X-Gbd-Timestamp` and
`X-Gbd-Signature: sha256=HMAC(secret, "<timestamp>.<body>")`, and are retried after 1 s, 5 s and 25 s.

### Permissions (ACL)

Front-ends send *who* is asking; gbd decides *what* they may do, so every front-end shares one policy.

| Field | Values |
|---|---|
| `principals` | exact strings, `*`, or prefixes like `discord:role:*`. The Discord bridge sends `discord:user:<id>`, `discord:role:<id>` (one per role), `discord:guild:<id>`, `discord:channel:<id>`, `discord:dm` |
| `bots` | `*`, a name or id, `account:name`, `account:*` |
| `actions` | `send` (send/ask, upload), `read` (log, files, cards), `approve` (cards, drafts), `admin` (new/stop/undo, raw calls) |

Rules are additive. A request without principals is the trusted local caller (`local`). Without an `acl` block
everything is allowed and gbd logs a warning. Because gb acts with **your** account, configure the ACL before
exposing any bot to other people.

## Discord bridge

`gbdiscord` is a ready-to-run bridge built on gbd. One Discord application fronts all of your bots.

```json
{
  "gbd": "unix:///home/USER/.config/gb/default/gbd.sock",
  "defaultBot": "Assistant",
  "servers": {
    "111111111111111111": {"bots": ["Researcher", "Writer", "Analyst", "Planner", "Reviewer"], "defaultBot": "Researcher", "alertChannel": "222222222222222222"},
    "333333333333333333": {"bots": ["Scout", "Editor", "Coder", "Tester", "Operator"], "defaultBot": "Scout"}
  },
  "avatars": {"Researcher": "https://example.com/researcher.png"},
  "guildId": ""
}
```

```sh
DISCORD_TOKEN=... gbdiscord config.json
```

Setup in the Discord Developer Portal: create one application with a bot, enable the **Message Content** intent,
and invite it with the **Manage Roles**, **Manage Webhooks**, Send Messages, Attach Files, Read Message History
and Add Reactions permissions.

| Behavior | Details |
|---|---|
| Per-server bots | Each server only sees the bots listed for it (`/bots`, mentions, `/ask`, autocomplete) |
| `@Bot` mentions | A mentionable role per bot is created in its servers, so `@Researcher` autocompletes. Mention several to ask them in parallel. Nobody holds these roles, so nobody gets pinged |
| Replies under the bot's name | Answers are posted through a channel webhook as "Researcher" (with `avatars` if set), with a link to the message they answer |
| Reply continuity | Using Discord's *Reply* on a bot's message continues **that** message (`replyToId`), even if the bot split its answer into parts |
| Other ways to address a bot | DM, mentioning the app, `[Bot Name] question`, `/ask prompt: bot:`, or a channel mapped in `channels` |
| Concurrency | No queue: a bot that is busy picks up new messages mid-turn; different bots run in parallel |
| Progress | 👀 when received, "typing…" while the bot works, ✅ when the turn is done |
| Cards | Approvals (once / always / deny), questions (buttons, or a select menu for multi-select, plus "Other…"), forms (modal, up to 5 fields), secret requests (modal, with a warning that the value passes through Discord), app access, drafts |
| Files | Discord attachments go to the bot; files the bot produces come back as uploads (see limits) |
| Alerts | Usage limits and similar alerts go to the server's `alertChannel` |
| Commands | `/ask`, `/bots`, `/new`, `/stop`, `/undo` |
| Permissions | Enforced by gbd's ACL (`approve` for cards, `admin` for `/new` `/stop` `/undo`) |
| Safety | Ignores bots and webhooks (no loops); output never pings `@everyone`, roles or users |

## File size limits

| Direction | Limit | Behavior |
|---|---|---|
| Discord → bot | Grok Bot accepts **25 MiB** per file, **200 MiB** for video. Discord lets people upload 10 MiB (free) up to 500 MiB (Nitro), so the Grok limit is the one that matters | Oversized files are skipped with a notice; the rest of the message is still delivered |
| bot → Discord | A bot can upload what the server allows: **10 MiB** by default, **50 MiB** at boost level 2, **100 MiB** at level 3 (`maxUploadMB` overrides) | Larger files are announced with their size and the command to fetch them (`gb files get`) |
| gbd | `/v1/upload` rejects oversized files with 413 before transferring; `/v1/files?max=` checks size before streaming | Files are streamed in 4 MiB chunks, never fully buffered in memory |

## Architecture

```
            ┌──────────────── your Linux server ────────────────┐
 browser ──▶│ gb login ──▶ Cursor auth (PKCE, poll)             │
            │                                                    │      ┌────────────────────────────┐
            │ gb (CLI) ─┐                                        │─────▶│ Cursor backend (Connect)   │
            │           ├─▶ box client ──▶ EnsureSandBox ────────│─────▶│  auth, box provisioning,   │
 Discord ◀─▶│ gbdiscord │      │                                  │      │  teams, server-hosted rooms│
            │   │       │      ▼                                  │      └────────────────────────────┘
            │   └─▶ gbd ┴─▶ gateway  POST /api/<method>  ─────────│─────▶┌────────────────────────────┐
            │       │  ▲    events   GET  /events (SSE)  ◀────────│──────│ your cloud box (gateway):  │
            │ hooks ▼  │ local API (unix socket / TCP+token)      │      │  bots, transcripts, files, │
            │  your backend                                      │      │  apps, automations         │
            └────────────────────────────────────────────────────┘      └────────────────────────────┘
```

- **Auth.** `gb login` runs the same PKCE device-style flow as the desktop app (open a URL, the CLI polls for the
  result), stores an access/refresh token pair and refreshes it before expiry. Refreshes are serialized with a file
  lock, so the CLI and the daemon can run side by side.
- **Box.** Each account has a cloud "box". `EnsureSandBox` returns its gateway URL and tokens, and wakes it if
  it sleeps. Gateway calls retry once on 401/403 (fresh connection) and wake the box on 417.
- **Gateway.** Almost everything is a JSON call to `POST {gateway}/api/<method>`; see
  [docs/GATEWAY_METHODS.md](docs/GATEWAY_METHODS.md). Live changes come over one SSE stream (`/events`) with ordered,
  per-agent sequence numbers; on a gap gbd refetches the tail and emits only what was missed.
- **Backend.** A few features (team people, group chats with people, Slack install links, run-time capabilities)
  are Connect RPCs on the backend; `gb rpc` reaches any of them.
- **gbd.** Keeps the roster in memory, turns raw transcript events into normalized events (message / card / file /
  turn.done / alert), attaches the caller's `ref`s, enforces the ACL and fans out to SSE subscribers and webhooks.
- **gbdiscord.** A thin adapter: Discord identity → principals, mentions/replies → bot + `replyToId`, events →
  persona messages, cards → components, files ↔ uploads.

More detail: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Security notes

- Everything runs with your account's permissions. Put an ACL in `gbd.json` before letting other people talk to
  your bots, and keep `approve`/`admin` for people you trust.
- `credentials.json`, `secrets.json` and `gbd.sock` are created with mode 0600 in a 0700 directory.
- Credentials are only sent to the expected hosts (`*.cursor.sh`, `*.cursor.com`, and `*.cursorvm.com` for the
  box), over HTTPS, and redirects are refused. `GB_ALLOW_LOCAL_GATEWAY=1` / `GB_ALLOW_ANY_GATEWAY=1` loosen this for
  development.
- `gb secret`, `gb form ... env:VAR` and `--set KEY=env:VAR` keep secrets out of shell history. Secret requests
  answered in Discord pass through Discord.
- gb never approves anything on its own.

## Known limitations

- **Run now** for routines is refused by the service for some server-hosted bots; `gb auto run --send` posts the
  routine's prompt instead.
- `reset` and `undo` are only available right after a failed or blocked turn, as in the app.
- Tool calls of server-hosted bots are not recorded in the transcript; progress is visible through `activity` events.
- Group chats with people require a Cursor team account (untested without one beyond the error path).
- Reply continuity in Discord uses an in-memory map from Discord messages to transcript entries. After a
  `gbdiscord` restart, replying to an older bot message is not routed; mention the bot or use `[Bot] …` instead.
- Desktop-only features are out of scope: screen viewing/control, teach recording, macOS Messages, Chrome cookie
  import, the 1Password app, passkeys, live voice.

## Building from source

Requires Go 1.22+.

```sh
git clone <this repository> && cd GrokBotCLI
go build -o gb .                                   # CLI + daemon
(cd examples/discord && go build -o ../../gbdiscord .)
go test ./... && (cd examples/discord && go test ./...)

# cross-compile for a Linux server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o gb-linux-amd64 .
```

Environment: `GB_HOME` (config dir), `GB_PROFILE` (account), `GB_DEBUG=1`, `CURSOR_ACCESS_TOKEN`,
`GB_GATEWAY_URL` / `GB_GATEWAY_TOKEN` / `GB_GATEWAY_NETWORK_TOKEN`.

---

<p align="center">
  Questions about this repository or a takedown request: <b>leeway@mail.grokbot.com</b>
</p>
