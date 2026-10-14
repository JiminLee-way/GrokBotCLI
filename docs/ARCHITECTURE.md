# Architecture

GrokBotCLI is a client. The bots themselves run in the service: in your account's cloud **box** or on the
service's own servers ("server-hosted" bots). This tool does what the desktop client does — sign in, connect to
the box, call its API, follow its event stream — without a desktop.

```
gb / gbd / gbdiscord
   │
   ├── Cursor backend (https://api2.cursor.sh, Connect RPC, JSON)
   │      sign-in, token refresh, EnsureSandBox, teams, group chats with people, Slack install links
   │
   └── Box gateway (https://<pod>.cursorvm.com)
          POST /api/<method>   JSON request/response   (docs/GATEWAY_METHODS.md)
          GET  /events         Server-Sent Events: data: {channel, payload}
          GET  /health
```

## Sign-in

1. `gb login` creates a PKCE verifier and challenge and prints
   `https://cursor.com/loginDeepControl?challenge=…&uuid=…&mode=login&redirectTarget=sand`.
2. You approve in any browser. The CLI polls `GET /auth/poll?uuid=…&verifier=…` (404 = not yet) with
   exponential backoff and receives `{accessToken, refreshToken}`.
3. The access token is a JWT; gb reads its `exp` and calls `POST /oauth/token`
   (`grant_type=refresh_token`) five minutes before it expires. A rotated refresh token replaces the old one; a
   `shouldLogout` answer ends the session.
4. `gb logout` calls `POST /auth/logout` and deletes local credentials.

Because nothing listens for a callback, the flow works unchanged on a server reached over SSH.

## Connecting to the box

`aiserver.v1.GrokBotService/EnsureSandBox {wake, supportsAsleepBox}` returns `gatewayUrl`, `gatewayToken`,
`networkToken` and `runState`. Gateway requests carry `Authorization: Bearer <gatewayToken>` and
`x-anyrun-network-token`. The connection is cached; on 401/403 it is re-provisioned once (single-flight, so many
concurrent calls trigger one refresh), and on 417 / `pod_hibernated` the box is woken and the call retried.

## Messages and transcripts

- `sendPrompt {agentId, prompt, clientNonce, replyToId?, attachmentPaths?}` returns `{accepted}`. Delivery is
  confirmed with `promptAcceptanceStatus {accountSlot: "host", clientNonce}` when the reply is inconclusive; a
  `not-found` result is the only case where resending (with the same nonce) is safe.
- A message to a bot that is already working joins its current turn. Different bots are independent.
- Transcript entries have kinds `message`, `send-message` (with ~26 sub-types: text, attachment, question
  widget, form, secret request, approval cards, drafts, …), `user-attachment`, `tool-call`, `notice`, `event`, ….
- `getAgentTranscriptTail {id, limit, beforeSeq?}` pages backwards with `nextBeforeSeq`.
- The `transcript` event channel delivers `appended` / `updated` entries with an ordered stamp
  `{replicaKey, epoch, sequence}` per agent. gbd applies them in order and refetches the tail on a gap.
- The `agent-upserted` channel carries `isRunning` / `isComposingMessage`, which drive typing indicators and
  `turn.done`. For server-hosted bots the roster turns idle slightly before the final message arrives, so gbd
  delays `turn.done` by 1.5 s.

## Cards

| Card | Answer |
|---|---|
| `auto-review-approval` | `resolveAutoReviewApproval {resolution: approved\|always\|denied, approvedCommand?}` |
| `local-tool-permission` | `resolveLocalToolPermission {resolution: allow-once\|always\|deny\|never}` |
| `widget` (question) | `respondToWidget {value}` — multi-select answers are newline-joined; `dismissWidget` |
| `user-form` | `submitUserForm {values}` / `dismissUserForm` |
| `secret-request` | `submitSecret {value}` |
| `connector-grant` | `resolveConnectorGrant {resolution: allowed\|always_this_bot\|always_all_team_bots\|skipped}` |

gb re-reads the transcript before answering and refuses cards that are no longer pending.

## Files

Uploads use `uploadAttachmentChunk` (4 MiB chunks) and return a box path that is passed in
`sendPrompt.attachmentPaths`. Bot files are box paths (or URLs) on `send-message` entries; they are read with
`readAttachmentChunk`. Limits follow the desktop client: 25 MiB per file, 200 MiB for video.

## Group chats

Bots-only groups are created on the box (`createGroup`). Groups that include people are created on the backend
(`CreateGrokBotRoom` with `humanMemberUserIds` from `DashboardService/GetTeamMembers`) and managed with
`AddGrokBotRoomPeople` / `RemoveGrokBotRoomPeople` / `SetGrokBotRoomMembers`. Without a name, gb builds the same
"A, B, and C" name the app would and omits `namedBy`, so the app keeps it in sync with membership.

## Apps (MCP connectors)

`getMcpCatalog` lists installable apps, `installMcpEntry` installs one, and `authenticateMcpServer
{oauthRedirectUri: http://localhost:<port>/callback}` returns the sign-in URL. The browser ends at that localhost
URL; its `code` and `state` are handed back with `completeMcpOAuth`. gb accepts them from a local listener, a
pasted URL, or notices the app becoming connected by itself.

## gbd internals

- **Roster** in memory, refreshed from `agents` / `agent-upserted` events; name→id resolution never needs a request.
- **Refs**: `POST /v1/send {ref}` is recorded before the request goes out, so the echo that races back is
  attributed. Every `message` / `turn.done` carries the refs of the turn.
- **Replay**: the last 1000 normalized events are kept; `GET /v1/events?since=<id>` resumes without gaps.
- **Hooks**: one goroutine per hook, at-least-once delivery, HMAC-signed.
- **ACL**: evaluated per request from the caller-supplied principals.

## Discord bridge internals

- One application, many personas: replies are sent with `WebhookThreadExecute` under the bot's name.
- `@Bot` is a mentionable role per bot (created on startup in each configured server); role ids map to bots.
- Every Discord message the bridge posts is mapped to `(agentId, entryId)`; a Discord *Reply* to it becomes
  `sendPrompt.replyToId`, which makes the bot answer about that exact message.
- Cards become buttons, select menus and modals; answers go through `POST /v1/cards/respond` with the user's
  principals.
