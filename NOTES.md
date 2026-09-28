# Revival notes (2026-09)

This branch (`revival-2026`) ports `mautrix-groupme` from the legacy
`maunium.net/go/mautrix/bridge` framework (last touched March 2023, pinned to
`mautrix v0.15.0`) to the current **bridgev2** framework in
`maunium.net/go/mautrix` (v0.31.0, September 2026). The legacy `bridge`
package no longer exists upstream, so this was a mandatory rewrite of the
bridge's Matrix-facing plumbing, not just a dependency bump. GroupMe-specific
business logic (message/attachment mapping, group vs. DM handling, likes)
was ported from the old `portal.go`/`user.go`/`puppet.go` rather than
rewritten from scratch, using `github.com/mautrix/gmessages` (an actively
maintained bridgev2 bridge from the same author/org) as the architectural
reference.

## Current status: compiles, builds, starts cleanly. Login + real-account deploy tried once; NOT fully integration-tested.

- `go build -tags goolm ./...` succeeds (pure-Go crypto, no libolm needed;
  useful for quick local iteration without `libolm-dev` installed).
- `go build ./...` (no tags) also works **inside the Docker image**, which
  installs `olm-dev`/`olm` via apk and links against real libolm — this is
  the build used for the shipped Docker image and is the recommended one for
  production (real libolm is better tested than the pure-Go fallback).
- `docker build -t groupme-bridge:revival2 .` succeeds using
  `golang:1.27-alpine3.23` as the build image (re-verified after the
  initial-sync and Faye HTTP/1.1 changes below; re-verified again as
  `groupme-bridge:revival4` after the websocket push transport change,
  see "Bayeux-over-websocket push transport" below).
- A live deploy against a real GroupMe account and Synapse homeserver
  confirmed login works end-to-end (access-token validation succeeds, bridge
  logs in as the real user), but **zero portal rooms were created**
  afterward. Root cause and fix: see "Initial chat sync" and "Faye/Bayeux
  push connection reliability" below. Neither fix has been verified against
  that live setup yet.
- The binary starts, connects to a local SQLite DB, runs bridge + Matrix
  state migrations successfully, generates an example config (`-e`) and an
  appservice registration (`-g`), and — when pointed at a fake/unreachable
  homeserver — retries the connection with backoff instead of crashing. No
  panics were observed in any of this. This was all verified by hand in this
  session; see the commands in the git log / PR description for exact repro
  steps.
- **Not verified**: an actual GroupMe account, an actual Matrix homeserver,
  or any live message flow. No GroupMe credentials or homeserver were
  available in this environment. Everything below "what's ported" should be
  treated as "compiles and follows the documented interface contracts, but
  has not exchanged a single real message."

## What's ported and should work in principle

- **Login**: a new `LoginFlowIDToken` flow (`pkg/connector/login.go`) asks
  for a GroupMe access token (same token model as before — GroupMe's REST
  API is still simple access-token auth, see below), validates it with
  `MyUser`, and stores it as bridgev2 `UserLogin` metadata.
- **Connect**: `pkg/connector/client.go` wires the existing
  `pkg/groupmeext` GroupMe API client + Faye push-subscription client
  (`github.com/karmanyaahm/wray`) into `NetworkAPI.Connect`/`Disconnect`.
- **Initial chat sync** (`pkg/connector/sync.go`, called from `Connect` in
  `client.go`): on every `Connect`, `GMClient.syncChats` lists the user's
  GroupMe groups (`IndexAllGroups`) and DM chats (`IndexAllChats`) via the
  REST API and queues a `ChatResync` (with `CreatePortal: true`) for each
  one, reusing the same `GetChatInfo`-based resync helper as the live
  push-triggered resyncs in `handlegroupme.go`. This used to be entirely
  missing: portals were only ever created reactively from live push events,
  so a freshly logged-in user got **zero** portal rooms until something
  happened to trigger a push for each chat. Runs in a background goroutine
  (detached from `Connect`'s context via `context.WithoutCancel`) so it
  doesn't block callers that invoke `Connect` synchronously (e.g. the
  unknown-error reconnect path in bridgev2 core). For DM chats this uses
  `Chat.OtherUser.ID` directly from `IndexChats`, which sidesteps the
  DM-portal-key heuristic problem described below for this code path
  specifically — the live push heuristic in `portalKeyForMessage` is
  unchanged and still has the caveat noted below.
- **Incoming messages** (`pkg/connector/handlegroupme.go`): GroupMe push
  events (`HandlerAll` from groupme-lib) are converted to bridgev2
  `simplevent` remote events — text messages, image attachments, likes
  (as a full reaction resync, since GroupMe's push payload doesn't say who
  added/removed a like, only the resulting list), and group name/topic/
  avatar/membership changes (all trigger a full `ChatResync` via
  `GetChatInfo`, matching the old bridge's "just resync everything" approach
  rather than incremental updates).
- **Outgoing messages** (`pkg/connector/handlematrix.go`): plain text only
  (matching the *previous* bridge's scope — outgoing media was never
  implemented pre-bridgev2 either), plus reactions (Matrix reaction <->
  GroupMe like, via `CreateLike`/`DestroyLike`).
- **Chat/user info** (`pkg/connector/chatinfo.go`): group name/topic/avatar/
  member list, DM peer name/avatar, ghost profile sync.
- **REST polling fallback** (`pkg/connector/poll.go`): a resilient
  fallback/replacement for incoming-message delivery via the REST API,
  running alongside the Faye push connection rather than instead of it —
  added because Faye has been observed failing persistently in production
  (see "Faye/Bayeux push connection reliability" and "REST polling
  fallback" below for the full writeup).

## What's NOT ported (known gaps)

- **Outgoing media** (images/files/locations from Matrix to GroupMe) — never
  existed in the old bridge either, so this isn't a regression, just an
  opportunity.
- **Incoming video/file/location attachments** — the old bridge's
  `handleAttachment` supported these (`portal.go` lines ~1008-1121 on
  `master`); only `image` attachments were ported to
  `convertGroupMeMessage` in `handlegroupme.go`. The logic to port is still
  visible in git history (`git show master:portal.go`).
- **Custom (double) puppeting via shared secret** — `custompuppet.go` and
  `tryAutomaticDoublePuppeting` in the old `user.go` were not ported.
  bridgev2 has its own double-puppeting support baked into the framework
  (`bridge.login_shared_secret_map` etc. in the generated config); it needs
  to be wired up/tested but wasn't in this session.
- **Provisioning API** (`provisioning.go`) — not ported. bridgev2 has a
  standard provisioning API surface; GroupMe-specific bits (if any were
  needed beyond login) would need porting.
- **Metrics / Segment analytics** (`metrics.go`, `segment.go`) — not
  ported. Low priority for a personal bridge.
- **Backfill** — not implemented (`BackfillingNetworkAPI` not implemented
  on `GMClient`). New portals will only show new messages after creation,
  not history.
- **Space rooms** (`GetSpaceRoom` in old `user.go`) — bridgev2's
  `personal_filtering_spaces` config option should cover this generically,
  but it hasn't been tested against this connector specifically.
- **Portal-key heuristic for DMs**: `GMClient.portalKeyForMessage` in
  `client.go` infers the DM "other user" as
  `msg.UserID != me ? msg.UserID : msg.RecipientID`. This mirrors the old
  bridge's general approach but wasn't validated against real GroupMe push
  payloads — GroupMe's DM `conversation_id` is actually a compound
  `"<id1>+<id2>"` string, and the old bridge had a `ParsePortalKey` helper
  to split it (see `database/portal.go` on `master`) that this heuristic
  does not replicate. **This is the single riskiest unverified piece** —
  it should be checked against real push payloads before relying on DM
  bridging.

## Dependency status

- `maunium.net/go/mautrix`: bumped `v0.15.0` -> `v0.31.0` (current as of
  2026-09-16). Go toolchain requirement bumped `go 1.19` -> `go 1.26.0`
  (toolchain `go1.27.1`, matching upstream's own `go.mod`). Go 1.27.1 was
  installed manually on this machine from https://go.dev/dl since apt's
  default was older.
- `github.com/beeper/groupme-lib`: **unchanged**
  (`v0.2.1-0.20221021205945-8f23e04eea71`). Checked upstream
  (github.com/beeper/groupme-lib) — the repo is **archived** and this pinned
  commit is already its last commit, so there is nothing newer to move to.
  GroupMe's REST API itself (dev.groupme.com) is still access-token based
  and hasn't publicly changed in a way that broke this client as far as
  could be determined without live credentials to test against.
- `github.com/karmanyaahm/wray` (Faye/Bayeux client for GroupMe's real-time
  push): **locally patched**, see below. Not archived, but last pushed
  2022-04-16. It's a small, self-contained implementation of the Bayeux
  protocol over HTTP long-polling and doesn't depend on mautrix-go, so the
  version bump didn't require touching it beyond adapting its logger
  interface (`pkg/groupmeext/subscription.go`) from the now-removed
  `maulogger/v2` to `zerolog` (which bridgev2 uses everywhere). Still used
  as the fallback transport, see "Bayeux-over-websocket push transport"
  below.
- `github.com/coder/websocket` **v1.8.15**: new direct dependency (was
  already present indirectly via `maunium.net/go/mautrix`). Used by the new
  `pkg/groupmeext/ws_faye.go` websocket Faye transport, see below.

## Faye/Bayeux push connection reliability (`push.groupme.com/faye`)

A live deployment against a real GroupMe account observed the Faye handshake
to `push.groupme.com/faye` reliably failing with `504 Gateway Timeout` /
connection timeouts, both from this bridge's Faye client (`wray`) and from a
plain `curl` run on the same host against the same endpoint. The `curl` used
HTTP/2 by default (curl's current default for https URLs) and got no
response at all; the suspicion going in was that GroupMe's push
infrastructure (or whatever fronts it) does not handle this POST-based
long-polling transport correctly over HTTP/2.

Investigation confirmed a concrete, fixable bug on our side that matches
this symptom: `wray`'s `HTTPTransport.send` (the function issuing every
Bayeux long-poll request) used `http.Post`, i.e. Go's
`http.DefaultClient`/`http.DefaultTransport`. Go's default transport
auto-negotiates HTTP/2 over TLS via ALPN whenever the server advertises
`h2` — so this client would have been making the exact same kind of HTTP/2
request that reproduced the hang via `curl`.

**Fix applied**: `wray` is now vendored locally at `thirdparty/wray/` (a copy
of the pinned upstream version, `v0.0.0-20210303233435-756d58657c14`, with
one functional change) and pulled in via a `go.mod` `replace` directive
(`replace github.com/karmanyaahm/wray => ./thirdparty/wray`). The patch in
`thirdparty/wray/http_transport.go` sends requests through a dedicated
`http.Client` whose `Transport.TLSNextProto` is set to a non-nil empty map,
which disables `net/http`'s automatic HTTP/2 upgrade and forces HTTP/1.1.
See `thirdparty/wray/README.md` for the full rationale, and why a local
`replace` was used instead of a real upstream fork (no ability to publish a
fork from this sandboxed environment — swap in a real fork later if one
exists).

**This fix has not been verified against the live push server** — the
sandboxed environment that wrote it has no live GroupMe credentials or
network path to `push.groupme.com`. It's a plausible, mechanistically sound
fix for the exact symptom observed (Go's automatic HTTP/2 upgrade matching
the `curl --http2` reproduction), but it's possible the push service was
simply down/degraded for an unrelated reason at the time of testing, in
which case this patch won't change anything. **Next deploy should check
whether the Faye handshake now succeeds** before assuming this is fully
resolved; if it still fails identically, the push service itself is the
more likely explanation and this patch (and `thirdparty/wray/`) can be
reverted.

A live deployment after the HTTP/1.1 patch above confirmed the 504s persist
identically — the Faye handshake has now failed on every attempt for over an
hour of continuous 10s-backoff retries, both before and after that fix. That
rules out the HTTP/2-vs-1.1 theory as the (sole) cause and points at
GroupMe's push infrastructure itself being degraded or blocked for this
connection, not a client-side bug. **This is why the REST polling fallback
below exists**: with Faye down, incoming messages (and even the logged-in
user's own messages sent from the native GroupMe app) had no path into
Matrix at all, since push was the only mechanism that ever fed new messages
into the bridge.

A related bug was found and fixed in the same investigation:
`GMClient.Connect` (`pkg/connector/client.go`) used to call
`gc.conn.SubscribeToUser(...)` — wray's Bayeux handshake, which retries
internally with its own backoff and has no timeout — **synchronously**,
before kicking off the initial chat sync goroutine. Since that handshake
was observed blocking for over an hour straight, this meant a dead Faye
server silently prevented the initial sync (and now, REST polling) from
ever starting too, even though neither actually depends on Faye succeeding.
`SubscribeToUser` is now called in its own goroutine, and chat sync + REST
polling are started unconditionally right after `Connect` sets up the Faye
listener, instead of after the handshake resolves. Faye is now purely an
optional low-latency accelerator: incoming messages get bridged over
whichever path (push or poll) sees them first.

## REST polling fallback (`pkg/connector/poll.go`)

Since the Faye push connection has proven unreliable in production (see
above) and was, at the time of writing, the *only* mechanism through which
new messages ever reached Matrix, `pkg/connector/poll.go` adds REST-API
polling as a resilient fallback/replacement, running alongside Faye rather
than instead of it.

- **What it polls**: `github.com/beeper/groupme-lib` (the pinned REST
  client, unchanged/archived — see "Dependency status" above) exposes
  `Client.IndexMessages(ctx, groupID, *IndexMessagesQuery)` for group
  messages (supports `SinceID`/`AfterID`/`BeforeID`/`Limit`) and
  `Client.IndexDirectMessages(ctx, otherUserID, *IndexDirectMessagesQuery)`
  for DMs (supports `SinceID`/`BeforeID`, keyed by `other_user_id`). Both
  are called directly (not through the pre-existing but unused
  `groupmeext.Client.LoadMessagesAfter` helper, which does the same thing
  but internally uses `context.TODO()` instead of a caller-supplied
  context — polling needs real context cancellation so the loop stops
  promptly on `Disconnect`).
- **Which chats get polled**: every poll tick re-lists the user's groups
  and DM chats via `gc.Client.IndexAllGroups()` / `IndexAllChats()` — the
  exact same calls `syncChats` (`sync.go`) uses for the initial sync — so
  newly created chats are picked up automatically without maintaining a
  separate chat list.
- **Interval**: configurable via the new `poll.interval_seconds` config key
  (`pkg/connector/config.go`, default **20s**, clamped to a 10s floor
  regardless of config). 15-20s was chosen per the task guidance: GroupMe
  doesn't aggressively rate-limit lightly-polled read endpoints for a
  single personal account, but there's no reason to poll faster than that,
  and the 10s floor guards against a config typo turning this into a tight
  request loop. `poll.enabled` (default `true`) turns polling off entirely
  if it's ever no longer needed.
- **"Last seen message ID" tracking**: reuses bridgev2's own message store
  instead of a new table — `DB.Message.GetLastNInPortal(ctx, portalKey, 1)`
  already returns the most recently bridged message for a portal (inserted
  there by *either* Faye or a previous poll), and that naturally survives
  bridge restarts since it's just the existing message history. If a
  portal has no bridged messages yet (e.g. one just created by initial
  sync with nothing pushed to it since), there's no cursor to poll forward
  from, so GroupMe's list endpoints are called with no since/after filter,
  which returns their default page of the most recent messages. This isn't
  a real backfill implementation (see "Backfill" below), but it's a
  harmless, bounded side effect: newly-synced empty portals opportunistically
  get a page of recent history instead of staying silent until something
  new arrives.
- **Conversion path**: every new message fetched by polling is passed to
  `GMClient.HandleTextMessage` — the exact same method the Faye push
  handler calls for live messages (`handlegroupme.go`) — so there is no
  separate/duplicated message-to-bridgev2-event conversion logic. This
  also means the logged-in user's own messages (sent from the native
  GroupMe app) are bridged with no special-casing: GroupMe's message-list
  endpoints return the same message shape (`UserID`/`RecipientID`/
  `GroupID`) as push payloads, so `HandleTextMessage`'s existing
  `IsFromMe`/portal-routing logic just works.
- **Dedup**: bridgev2 core already dedupes incoming `RemoteEventMessage`s
  by message ID before doing anything observable
  (`Portal.handleRemoteMessage` → `DB.Message.GetAllPartsByID`, in
  `maunium.net/go/mautrix/bridgev2/portal.go`) — if the ID already has a
  bridged message, the event is silently ignored. Since both the Faye
  handler and the poller feed `networkid.MessageID`s derived the same way
  (`MakeMessageID(msg.ID)`, the raw GroupMe message ID) into the same
  event type, this dedup applies uniformly regardless of source: whichever
  of Faye/polling sees a given message first wins, and the other is a
  no-op. No polling-specific dedup logic was needed.
- **Lifecycle**: the poll loop is started in `GMClient.Connect` as its own
  goroutine with a cancellable context (`gc.pollCancel`), and stopped in
  `GMClient.Disconnect`; it does not depend on the Faye handshake
  succeeding or even being attempted (see the `Connect` restructuring
  above).

**Not verified live**: like the rest of this branch, this was written and
built (including a full Docker build with real libolm) without live
GroupMe credentials, so the actual REST calls, their response shapes, and
real-world rate-limit behavior have not been exercised against
`api.groupme.com`. On next deploy, check that: portals that were empty
after initial sync pick up a page of recent messages within one poll
interval; a message sent from another client into an existing chat shows
up in Matrix within ~20s even with Faye still down; and a message sent
from the native GroupMe app by the bridge's own account also shows up
(this was the original trigger for this work). If Faye recovers at some
point, also confirm a message it delivers doesn't show up twice.

With this change, "no reliable message delivery when Faye is down" (the
problem that motivated this work) should be addressed: message delivery no
longer depends on Faye succeeding at all. What's *not* addressed is
real-time latency when Faye is down — polling caps latency at roughly one
poll interval (~20s) instead of push's sub-second delivery — but the task
explicitly treats that as an acceptable tradeoff for a resilient fallback,
not a regression to fix.

**Update (2026-09-18): root cause identified as long-polling being
deprioritized/degraded server-side, not (only) HTTP/2.** The live
confirmation above that the HTTP/1.1 patch didn't change the 504 behavior
at all was the first strong signal that HTTP/2 wasn't the (sole) cause.
Re-checking GroupMe's current docs turned up a detail the original
investigation missed: `dev.groupme.com/tutorials/push` now explicitly says
"you can perform long-polling over HTTP, but we recommend dropping down to
websockets if you have the option," and the community-maintained protocol
docs (https://groupme-js.github.io/GroupMeCommunityDocs/api/ws/) show the
*current* handshake payload declaring
`"supportedConnectionTypes": ["websocket"]`, not `["long-polling"]`. Both
were fetched directly (not from memory/paraphrase) on 2026-09-18 to confirm
the literal JSON before implementing anything against them. `wray` (our
Faye client, vendored at `thirdparty/wray/`) has zero websocket support —
it's HTTP-long-polling-only. The working theory is that GroupMe's backend
has deprioritized the long-polling transport path in favor of websocket,
which would produce exactly the symptom seen (hangs/504s instead of a
clean protocol-level rejection). The HTTP/1.1 patch above is still kept
(it's a real, independently-justified fix, and is also now applied to the
websocket dial handshake for the same reason — see below) but is no longer
assumed to be sufficient by itself. The REST polling fallback above and
the websocket transport below are complementary, not alternatives: polling
guarantees delivery (bounded by its interval) regardless of which push
transport (if either) is working; websocket is an attempt to restore
actual real-time push on top of that safety net.

## Bayeux-over-websocket push transport (new, 2026-09-18)

Real-time push now has two transport implementations behind the same
`groupme.FayeClient` interface (`Listen()` / `WaitSubscribe(...)` from
`github.com/beeper/groupme-lib`'s `real_time.go`):

- `groupmeext.FayeClient` (`pkg/groupmeext/subscription.go`) — the
  pre-existing wray-based HTTP long-polling client.
- `groupmeext.WSFayeClient` (`pkg/groupmeext/ws_faye.go`, **new**) — a
  from-scratch Bayeux client that speaks the same protocol over a
  websocket connection to `wss://push.groupme.com/faye` (same host/path as
  the HTTP transport, confirmed against both doc sources above — only the
  scheme and `supportedConnectionTypes` differ).

**Library choice**: `github.com/coder/websocket` (formerly `nhooyr.io/websocket`),
pinned at `v1.8.15`. It was already present in `go.mod` as an *indirect*
dependency — pulled in transitively by `maunium.net/go/mautrix`'s own
appservice websocket transport (`maunium.net/go/mautrix@v0.31.0/appservice/websocket.go`)
— so using it directly here adds no new dependency to the build, reuses a
version already exercised by the upstream mautrix-go project, and has a
smaller/simpler API surface than `gorilla/websocket` (context-based
Read/Write, no manual ping/pong plumbing needed for this use case). It's
now promoted from an indirect to a direct `require` in `go.mod` via
`go mod tidy`.

**Protocol implementation** (verified against the literal JSON from both
doc sources fetched in this session, not paraphrased):

- Handshake: `{"channel":"/meta/handshake","version":"1.0","supportedConnectionTypes":["websocket"],"id":"<n>"}`,
  sent as a one-element JSON array (the Bayeux spec's message envelope is
  always an array regardless of transport — matches what `thirdparty/wray`
  already does for the HTTP POST body).
- Subscribe: `{"channel":"/meta/subscribe","clientId":"<id>","subscription":"<channel>","id":"<n>","ext":{"access_token":"...","timestamp":...}}`.
  The `ext` auth field is populated by calling `groupme.OutMsgProc` from
  `real_time.go` directly (same function the wray path uses via its
  `AuthExt` extension) instead of reimplementing the
  access_token/timestamp logic — this was an explicit goal so the two
  transports can't drift on auth behavior.
- Connect/heartbeat: `{"channel":"/meta/connect","clientId":"<id>","connectionType":"websocket","id":"<n>"}`,
  sent in a continuous cycle (next connect fires as soon as a response to
  the previous one arrives), per the Bayeux spec's liveness/advice
  mechanism — the websocket itself doesn't need this to receive pushed
  messages (those can arrive as independent frames at any time), but
  `advice.reconnect` values (`"none"`/`"handshake"`) are only delivered via
  connect responses, so it's kept running to honor server-directed
  reconnect/rehandshake requests.
- Server-pushed events (message/like/membership/etc.) arrive as ordinary
  Bayeux data messages on channels like `/user/<id>`, `/group/<id>`,
  `/direct_message/<id1>_<id2>`; these are matched against the
  channel->`chan groupme.PushMessage` map populated by `WaitSubscribe` and
  written directly into it. From there they flow into
  `groupme.PushSubscription`'s existing dispatch goroutine
  (`StartListening` in `real_time.go`) exactly like long-polling messages
  do, reaching the same `RealTimeHandlers`/`HandlerAll` methods already
  implemented on `GMClient` in `pkg/connector/handlegroupme.go` —
  **no changes were made to `handlegroupme.go` or to `groupme-lib`** for
  this; the whole point of implementing `groupme.FayeClient` /
  `groupme.PushMessage` was to hook into that existing, already-wired
  dispatch path rather than duplicating it.
- The websocket dial's HTTP client also has `TLSNextProto` forced to
  disable HTTP/2, mirroring the `thirdparty/wray` patch, since RFC 6455's
  `Connection: Upgrade` handshake isn't valid under HTTP/2 and the same
  host already showed HTTP/2-related hangs for the long-polling path.
- Reconnection: `WSFayeClient.Listen()` loops forever, redialing +
  re-handshaking + resubscribing all previously-registered channels with
  exponential backoff (1s doubling to a 60s cap) on any failure — the
  `FayeClient` interface has no explicit stop/close signal, matching the
  pre-existing wray client's contract (`GMClient.Disconnect()` just drops
  its reference; see the comment there).

**Transport selection / fallback** (`pkg/connector/client.go`,
`GMClient.selectFayeClient`): on every `Connect()`, a `WSFayeClient` is
created and probed with a one-shot dial+handshake (`WSFayeClient.Probe`,
20s timeout) *before* being handed to `PushSubscription.StartListening`.
If the probe succeeds, that same client is reused for the real connection
(websocket is primary). If it fails for any reason, the code falls back to
constructing the pre-existing wray-based long-polling `FayeClient`, so a
websocket-specific outage or a network that blocks the upgrade doesn't
take down push entirely. This was chosen over a fully dynamic
runtime-switching design (e.g. falling back mid-connection after Listen()
has already started) as a deliberate scope tradeoff — a clean one-shot
probe-then-commit covers the realistic failure mode (transport unreachable
at connect time) without adding the complexity of tearing down and
re-homing an in-flight `PushSubscription` mid-session.

**What's verified vs. not**: `go build -tags goolm ./...`, `go vet -tags
goolm ./...`, and `docker build` (using real libolm, no build tag) all
succeed with this change — the protocol implementation is structurally
correct per the current documented JSON formats and compiles/type-checks
against `groupme-lib`'s real interfaces. **None of this has been exercised
against the live `push.groupme.com/faye` endpoint** — no GroupMe
credentials or network path were available in this session either (same
limitation as every previous session in this investigation). The specific
things a live deploy should check first: (1) does the websocket handshake
actually succeed where long-polling hung/504'd — this is the whole premise
of this change; (2) does GroupMe's server-pushed data land in the plain
(non-array) object shape assumed by `decodeBayeuxFrame`'s fallback path, or
always as arrays — the code handles both, but the actual framing was never
observed live; (3) does the `/meta/connect` continuous cycle behave
sanely over a persistent socket (no unexpected rate limiting from sending
one every response-turnaround) — the Bayeux spec doesn't mandate a
different cadence for websocket vs. long-polling, but GroupMe's specific
server behavior here is unconfirmed; (4) whether the HTTP/2-disabling dial
client is even necessary for the websocket path (unlike the long-polling
case, no independent `curl` reproduction of a websocket-specific hang
exists yet — it was applied preemptively based on the same host and the
general RFC 6455-vs-HTTP/2 mismatch, not a directly observed failure).

## Repository layout changes

- `main.go`, `user.go`, `portal.go`, `puppet.go`, `matrix.go`, `commands.go`,
  `custompuppet.go`, `provisioning.go`, `metrics.go`, `segment.go`,
  `messagetracking.go`, `bridgestate.go`, `formatting.go`, `database/`,
  `config/` — all removed. These implemented the legacy `bridge` package's
  interfaces (`bridge.Bridge`, a hand-rolled SQL `database` package, a
  hand-rolled `config` package built on `bridgeconfig.BaseConfig`), none of
  which exist in bridgev2. Their logic is preserved in git history on
  `master`/`upstream/master` for reference while porting the remaining gaps
  above.
- `groupmeext/` moved to `pkg/groupmeext/` (only the logger interface
  changed, from `maulogger/v2` to `zerolog`).
- New: `pkg/connector/` (the bridgev2 `NetworkConnector` implementation —
  this replaces `main.go` + `user.go` + `portal.go` + `puppet.go` +
  `config/`), `cmd/mautrix-groupme/main.go` (replaces old `main.go`,
  now a thin wrapper around `mxmain.BridgeMain`).
- bridgev2 owns its own database schema/migrations (Postgres or SQLite);
  the old hand-rolled `database/` package and its `upgrades/*.sql` are gone.
  Network-specific metadata (GroupMe access token, portal type) is stored
  via `GetDBMetaTypes()` in `pkg/connector/dbmeta.go` instead.

## Fallback plan

If further bridgev2 work stalls, the documented fallback (per the task that
produced this branch) is the older Node.js
[matrix-puppet-groupme](https://github.com/matrix-hacks/matrix-puppet-groupme)
bridge, which talks to GroupMe's stable access-token REST API directly and
doesn't need this level of framework rework, at the cost of a much
older/simpler architecture (no double puppeting, no bridgev2 features like
built-in provisioning/backfill/space rooms).

## Suggested next steps

1. Get real GroupMe credentials and a test Matrix homeserver, then actually
   exercise login, an incoming group message, an incoming DM, an outgoing
   message, and a like/reaction in both directions. In particular, confirm
   the initial chat sync (see above) actually creates a portal for every
   existing group/DM after login, and check whether the new websocket push
   transport (see "Bayeux-over-websocket push transport" above) actually
   handshakes successfully against `wss://push.groupme.com/faye` where the
   long-polling path hung/504'd — this is the single most important thing
   to verify live, since it's the entire premise of that change and
   nothing about it has been exercised against the real server yet. Check
   the logs for "Using GroupMe push websocket transport" (success) vs.
   "falling back to HTTP long-polling transport" (probe failed) from
   `GMClient.selectFayeClient` in `pkg/connector/client.go`. Also confirm
   the REST polling fallback (`pkg/connector/poll.go`, see "REST polling
   fallback" above) actually delivers new messages — including the bridge's
   own account's messages sent from the native app — within one poll
   interval, and that an empty portal picks up a page of recent history on
   its first poll. Between the two, message delivery should now work even
   if the websocket probe falls back to long-polling and long-polling is
   still degraded/down.
2. Fix the DM portal-key heuristic (see above) once real push payloads are
   available to confirm the actual shape of `ConversationID`/`ChatID` for
   DMs vs. groups. Note the initial sync path (`pkg/connector/sync.go`)
   doesn't hit this problem since it gets `OtherUser.ID` directly from
   `IndexChats`; only the live push handler (`portalKeyForMessage` in
   `client.go`) still has the heuristic.
3. Port video/file/location attachment handling from `master`'s
   `portal.go` `handleAttachment` (git history has the full implementation).
4. Wire up double puppeting (`bridge.login_shared_secret_map`) and confirm
   it works with bridgev2's built-in support.
5. Consider implementing `BackfillingNetworkAPI` so newly created portals
   get recent history instead of starting empty. The REST polling fallback
   (`pkg/connector/poll.go`) opportunistically delivers one page (~20) of
   recent messages the first time it polls a portal with no bridged
   history yet, as a side effect of how it seeds its "since" cursor — see
   "REST polling fallback" above — but that's incidental, not a real
   backfill implementation (no pagination past one page, no user-facing
   config, no distinction from a live message for e.g. notification
   purposes).
6. If the Faye HTTP/1.1 patch (`thirdparty/wray/`) is confirmed to fix the
   push handshake, consider upstreaming it as a real fork/PR against
   `github.com/karmanyaahm/wray` instead of carrying a local `replace`
   indefinitely. If it *doesn't* fix the handshake, that's strong evidence
   the problem is external (GroupMe's push service itself), and the patch
   can be reverted.

## WebSocket reconnect-cycle fix (2026-09-19)

The websocket push transport (see "Bayeux-over-websocket push transport"
above) worked but reconnected roughly every 45 seconds instead of staying
open — self-healing each time (~2s), so not a correctness problem, but not
what "stable" should look like either.

Root cause: `connectLoop` (`pkg/groupmeext/ws_faye.go`) wrapped the wait
for GroupMe's `/meta/connect` response in a 45-second `connCtx` timeout and
treated a timeout as fatal, forcing a full reconnect. But `/meta/connect`
is a long-poll-style endpoint by design under Bayeux — GroupMe holds it
open until there's something to deliver, so a slow/quiet response is
normal, not a sign of a dead connection. The 45s ceiling was just
rediscovering that fact every time and reconnecting needlessly.

Fix: removed the timeout on waiting for the response (only the *send* of
the connect request is still bounded, at 15s — that one should be fast).
Real liveness detection was added separately: a `pingLoop` that sends an
actual WebSocket-level `conn.Ping` every 30s (10s timeout), wired into
`connectAndRun`'s `select` via a new `pingErrCh` — so a genuinely dead
connection is still caught quickly, just via the transport's own liveness
primitive instead of misreading a quiet Bayeux response as one.

Verified live: 9+ minutes of continuous connection, zero reconnects, after
this landed (previously: cycling every ~45s indefinitely).

## Health-check/alerting system, and a REST polling rate-limiting bug it caught (2026-09-19)

Added a small out-of-band monitoring layer, not part of the bridge itself:
a systemd oneshot (`matrix-health-check.service` + `.timer`, every 5 min)
running `/matrix/health-check/check.sh`, which checks that all relevant
systemd units (Synapse, each bridge, Postgres, etc.) are active and greps
`journalctl --since <last run>` for `\bERR\b|\bFATAL\b|panic`. On a
problem, it posts to a dedicated Matrix room via curl using the
double-puppet `as_token` (same one already configured for double
puppeting — no new credential). State (last-checked timestamp per unit) is
tracked in `/matrix/health-check/state/`.

This isn't part of the bridge's own code/this repo — it lives on the host
at `/matrix/health-check/` — but it's documented here because within
minutes of going live, it caught a real bug: the GroupMe bridge was
logging `Err()`-level lines that turned out to be genuine GroupMe API
rate-limit responses (`Error Code 429`), not one-off noise.

**Root cause**: an earlier fix this branch made (see "REST polling
fallback" above, the reaction-polling-skipped-on-no-new-messages fix)
merged what had been two REST requests per chat per poll tick into one —
correct on its own, but with ~80 chats at the then-current 20s poll
interval, even *one* request per chat per tick is ~4 req/s, all fired
back-to-back at the top of every tick. GroupMe's rate limiting reacts to
that bursty pattern specifically, not just total steady-state volume.
Confirmed precisely by counting the literal string `Error Code 429` in
the logs (an earlier, looser check that just grepped for the substring
`429` gave misleadingly high/noisy counts, since that also matches
timestamps and IDs elsewhere in log lines) — 362 and 500 genuine 429s
across two consecutive runs at the old settings. Disabling polling
entirely (`poll.enabled: false` in the live config) immediately dropped
that to 0, confirming polling itself (not push/resync traffic) as the
cause.

**Fix** (`pkg/connector/poll.go`, `client.go`, `thirdparty/groupme-lib/data_types.go`):
- Stagger the per-chat requests across 80% of the poll interval instead of
  firing them all at once (`pollOnce`).
- Back off a specific chat for 3 minutes after an actual 429/420 from it,
  instead of retrying on the very next tick regardless
  (`checkPollBackoff`/`maybeBackoffPoll`, backed by a new `pollBackoff`
  map on `GMClient`, `client.go`).
- Raise the default/minimum poll interval to 60s/30s (was 20s/10s) to cut
  steady-state request rate independent of the above.
- Added `groupme.HTTPTooManyRequests = 429` to the vendored library, which
  only had the older `HTTPEnhanceYourCalm = 420` the original author
  apparently expected instead — 429 is what GroupMe actually returns live.
- `logPollError` now logs 429/420 at `Warn()`, not `Err()`, since
  `maybeBackoffPoll` already self-mitigates it — an `Err()` line implies
  something needs attention, which would otherwise also falsely trip the
  health-check's own ERR-pattern grep on an already-handled condition.

Verified live: 20 consecutive 90-second windows (~6.5 min total) at 0
genuine 429s after deploying, versus hundreds before. Live config
(`/matrix/mautrix-groupme/data/config.yaml`, not in git) updated to
`poll.enabled: true`, `interval_seconds: 60` to match.

This is a good example of why the health-check system was worth building
beyond just "peace of mind": it surfaced a real, previously-invisible
problem (the bridge was silently getting rate-limited in production)
within minutes of being deployed, well before it would have been noticed
any other way.

## Sustained websocket-outage alerting (2026-09-19)

Follow-up gap found while explaining the push/polling architecture: with
the reconnect-cycle fix above landed, a *single* websocket reconnect is
expected and harmless (self-heals in ~2s, logged at Warn). But a
*sustained* outage — websocket genuinely down for an extended period,
not just one blip — produced zero signal to the health-check alerting:
the bridge process doesn't crash (REST polling keeps delivering messages
independently, just delayed up to the poll interval instead of
near-instant), and every reconnect-related log line in `ws_faye.go` is
deliberately Warn, not Error, specifically because a single reconnect
isn't failure-worthy. Net effect: a websocket down for hours would be
completely invisible unless someone went and read the logs — no data
loss, but no visibility either.

Fixed in `pkg/groupmeext/ws_faye.go`: `WSFayeClient` now tracks
`lastConnected`, reset on every successful Bayeux handshake
(`markConnected`, called from `connectAndRun`). If `Listen`'s reconnect
loop finds more than 5 minutes have passed since the last successful
handshake, it logs one Error-level line (`maybeLogSustainedDegradation`)
— which the health-check script above already greps for, so this needed
no changes on that side at all. Repeats every 15 minutes if the outage
continues, rather than alerting once and going silent for a multi-hour
outage.

Verified: builds, deploys cleanly, websocket handshake still succeeds
immediately on a normal restart (no regression to the happy path). The
alerting path itself (an actual 5+ minute outage) hasn't been separately
forced/tested live — would need e.g. blocking outbound access to
`push.groupme.com` temporarily to verify end-to-end, not done here since
that would have disrupted real-time delivery unnecessarily for something
already reasoned through carefully.

## Incoming video/file/location attachments (2026-09-20)

Closed the "Incoming media limited to images" known gap. Ported
video/file/location handling from the pre-2023 bridge's `handleAttachment`
(`portal.go` on `master`) into `convertGroupMeMessage`
(`pkg/connector/handlegroupme.go`), which previously only handled the
`image` attachment type and silently skipped everything else.

`groupme.Attachment.Type` values, confirmed against the old bridge's own
switch statement (the only place this was ever documented): `image`
(already ported), `video`, `file`, `location`, plus `mentions`/`emoji`/
`reply` which ride alongside `msg.Text` rather than needing their own
message part — the new `default` case in the switch just skips those,
same as it always implicitly did for anything unhandled.

Per-type notes:
- **video**: GroupMe's video CDN wants the account's access token as a
  `token` *cookie*, not the `X-Access-Token` header every other GroupMe
  API endpoint uses — this is exactly what the old bridge did, so it's
  assumed correct, but wasn't independently re-derived or re-verified
  against current GroupMe behavior.
- **file**: a two-step API — POST to `file.groupme.com/v1/{groupID}/fileData`
  for metadata (name, mime type), then POST to
  `.../v1/{groupID}/files/{fileID}` for the actual bytes, both
  authenticated via `X-Access-Token`. Group-only: GroupMe's file-sharing
  feature has no DM equivalent, and the API is keyed by group ID (not
  conversation ID), so `convertGroupMeMessage` skips a `file` attachment
  on a DM (`msg.GroupID` empty) with a warning rather than guessing at an
  ID that wouldn't work anyway.
- **location**: pure formatting, no network call — parses `lat`/`lng`
  into an `event.MsgLocation` with a `geo:` URI.

`convertGroupMeMessage`'s signature gained a `token string` parameter
(the account's own GroupMe access token, `gc.Meta.Token`) since video/file
downloads need it and image downloads (a plain public GET) didn't
previously need to pass anything like it through.

**Real bug fixed along the way, not just a port**: the old bridge's
`DownloadFile` (`pkg/groupmeext/message.go`) called `panic(err)` on any
HTTP request failure — a transient network error fetching one file
attachment would have taken down the *entire bridge process*, disconnecting
every chat, not just failing that one message. Rewritten (along with
`DownloadVideo`, for consistency) to return an error like every other
attachment path already does, so a failed download now just skips that one
message (logged as a warning) instead of crashing everything. Also dropped
a dead line in the old code (`req.URL.Query().Add(...)`) that mutated a
copy of the URL's query and was never actually applied to the request —
a no-op even in the original, presumably vestigial from some earlier
version of that endpoint's auth.

**Verification, without sending anything to Matrix** (per explicit
instruction): confirmed first, by reading `bridgev2/portal.go`'s
`handleRemoteMessage`, that bridgev2 core dedupes incoming messages by ID
*before* ever calling `ConvertMessageFunc` — meaning re-polling the
account's existing (already-bridged) history can't be used to exercise
this new code at all, and deploying it live is safe/inert for every
existing chat, only taking effect on genuinely new incoming attachments
going forward. To actually test it, wrote a small standalone Go program
(not committed — lived briefly at `cmd/attachtest/`, deleted after use)
that used `groupmeext`/`groupme-lib` directly to scan the real account's
message history via GroupMe's REST API — entirely independent of
bridgev2/Matrix, a pure read — for any existing video/file/location
attachments, and ran the new download functions against any found:

- Found one real `file` attachment (a PDF in an existing group) and
  successfully downloaded it via the new `DownloadFile` — 125277 bytes,
  correct filename ("BEAR 26.pdf") and mime type (`application/pdf`)
  recovered from GroupMe's own metadata endpoint. This path is now
  confirmed working end-to-end against production data.
- Scanned 28 groups' most recent 100 messages each and all 53 DMs' full
  history; found zero `video` or `location` attachments to test against.
  Those two remain ported, code-reviewed, and building/deploying cleanly,
  but **not independently live-verified** — worth confirming the next
  time either type actually shows up in the account (or borrowing a test
  account/group that has one).

## GroupMe polls (2026-09-20)

Prompted by the user creating a real test poll and noticing it only
bridged as GroupMe's bare system text ("Created new poll 'test'"), no
options visible from Matrix.

GroupMe's polls feature (and its API) postdates the pinned groupme-lib
entirely — no `Attachment`/`Message` field for it existed, and it isn't
covered anywhere on dev.groupme.com's v3 docs. Reverse-engineered the real
wire format directly against the live account (read-only REST calls, no
polls created/voted on/modified in the process):

- A poll's lifecycle produces messages carrying a top-level `event` field,
  not previously modeled at all — added as `Event`/`PollEventData`/
  `PollEventOption`/`PollEventEntity` in `thirdparty/groupme-lib/json.go`.
  Three `event.type` values observed live:
  - `poll.created`: a normal message from the creating user, `text:
    "Created new poll 'X'"`, an `attachments: [{type: "poll", poll_id}]`
    entry (new `Poll` attachmentType constant — the ID alone isn't enough
    to render anything, the real content is in `event`), and
    `event.data` carrying just the poll's `id`/`subject` plus the
    creating user's id/nickname. Getting the actual question's options
    requires a separate call.
  - `poll.reminder`: a system message (`sender_type: "system"`, `text:
    "Poll 'X' is about to expire"`), `event.data.poll` additionally
    carries `expiration` (unix timestamp).
  - `poll.finished`: a system message (`text: "Poll 'X' has expired"`),
    `event.data.options` carries the final tally directly — each option's
    `title`, `votes` count, and (unconfirmed for a non-anonymous poll,
    every poll observed live was anonymous) possibly `voter_ids`. No
    extra API call needed for this one; the finished message already has
    everything.
- To get an in-progress poll's actual question/options (`poll.created`
  alone isn't enough), added `Client.GetPoll(ctx, conversationID,
  pollID)` (new `thirdparty/groupme-lib/poll_api.go`), hitting `GET
  /poll/{conversationID}/{pollID}` — also undocumented, reverse-engineered
  against a real live poll. Confirmed fields: `subject`, `options`
  (`id`/`title` pairs), `status` ("active" confirmed; a finished poll's
  own status wasn't checked since `poll.finished`'s embedded data already
  covers that case), `type` ("single" confirmed; "multiple" for a
  multi-select poll is expected from GroupMe's UI but not confirmed
  against a real response), `visibility` ("anonymous" confirmed; a named
  equivalent not confirmed), `expiration`.

`convertGroupMePollEvent` (`pkg/connector/handlegroupme.go`) renders all
three event types as a single readable text message part — the question
and options for `poll.created` (fetched live via `GetPoll`, falling back
to just the bare subject if that call fails so the message doesn't
disappear entirely), the final vote-by-option tally for `poll.finished`,
and a plain notice for `poll.reminder`. Hooked into
`convertGroupMeMessage` before the normal attachment loop, since a poll
message's real content lives in `Event`, not in its (sometimes absent,
sometimes just-a-pointer) attachments.

**Deliberately one-way and plain-text.** Matrix has a native interactive
poll widget (MSC3381, e.g. rendered/votable in Element), which this does
not use. Wiring that up for real two-way sync — a Matrix vote calling
GroupMe's (unresearched) vote-casting endpoint, a GroupMe vote change
updating the Matrix poll's live state, handling a poll closing from
either side — would be a substantially larger feature than every other
attachment type this bridge bridges (all one-way, GroupMe → Matrix only).
Out of scope for now; the goal here was just making a poll's
question/options/results legible from Matrix, matching what triggered
this work.

**Verified without creating, voting on, or otherwise modifying any real
poll**: wrote a throwaway `go test` inside `pkg/connector/` (same pattern
as the video/file/location verification above — written, run, and
deleted, never committed) that called `convertGroupMePollEvent` directly
with real payloads:
- The user's actual still-active test poll (`GetPoll` really was called
  live against it — a read, not a write) rendered as:
  `📊 New poll: "test"` / `• 1` / `• 2` / (blank line) /
  `Vote in the GroupMe app (anonymous voting). Closes Sep 20, 1:59 AM.`
- A historical finished poll (fully self-contained fixture data, no
  network call) rendered as:
  `📊 Poll ended: "Are you available from 5:00 P.M. to 6:00 P.M. this
  Saturday?"` / `• Yes — 47 votes` / `• No — 13 votes`
- A historical reminder rendered as:
  `📊 Poll "..." is about to close.`

All three matched expectations on inspection. Deployed live; since
bridgev2 dedupes by message ID before conversion runs (see "Incoming
video/file/location attachments" above), this is inert for the
already-bridged `poll.created` message from the user's test poll — it'll
only render richly the next time a *new* poll event comes through (e.g.
when that same test poll's `poll.finished` event eventually fires, since
that message ID hasn't been seen yet).

## Outgoing location, and outgoing video/file attachments (2026-09-20)

Follow-up to "Outgoing image attachments" (see NOTES.md/git history):
extended outgoing media to also cover `m.location`, and investigated (but
did not implement) outgoing video/file.

**Outgoing location** (`matrixLocationToAttachment`,
`pkg/connector/handlematrix.go`): unlike image, no upload or API call is
needed at all -- a GroupMe location attachment is just `{type: "location",
lat, lng, name}` embedded directly in the outgoing message JSON, same as
every other attachment field already sent via the existing
`CreateMessage`/`CreateDirectMessage` calls. Parses the outgoing event's
`content.GeoURI` (an RFC 5870 `geo:` URI), handling the RFC's optional
altitude coordinate and `;u=<uncertainty>` suffix (GroupMe's format has no
room for either, so anything past the first two coordinate fields is
dropped) -- mirrors the parsing already done for the *incoming* direction
in `handlegroupme.go`'s `convertGroupMeMessage`.

Verified without sending anything: `matrixLocationToAttachment` is a pure
function (no network or Matrix calls needed, unlike image), so it was
exercised directly via a throwaway `go test` (written, run, deleted, never
committed) covering: a plain `geo:lat,lng`, one with a `;u=` suffix, one
with an altitude component, and a deliberately malformed URI expected to
error. All four behaved correctly.

**Outgoing video/file: investigated, not implemented.** Searched for a
documented or community-reverse-engineered upload endpoint the way images
have one (`thirdparty/groupme-lib/image_service.go`,
`image.groupme.com/pictures`) and incoming video/file downloads do
(`push`/`file.groupme.com`, see "Incoming video/file/location
attachments" above) -- found nothing usable. GroupMe's official
`dev.groupme.com` v3 docs don't cover it at all (same as polls), and
unlike polls -- where the real wire format could be reverse-engineered
by just *reading* real messages the account had already received -- there
was no existing real outgoing video/file message in the account's history
to learn an upload flow from (the incoming-side investigation for those
types found zero examples of either in the account's own history either,
see "Incoming video/file/location attachments" above). Web search turned
up community acknowledgment that this is genuinely undocumented territory
(e.g. an old GroupMe API support forum thread asking for video upload
support that was never answered), not a known-but-unwritten-up endpoint.

Deliberately didn't guess at an endpoint shape and ship something
untested -- unlike everything else in this bridge, there was no real data
point (a live account example, a documented API, or even a solid
community write-up) to build against here, only speculation. The
realistic next step, if this is wanted, is a packet capture of the actual
GroupMe mobile app sending a real video or file attachment -- something
that needs a human with the app and a device to do, not something
achievable from this environment alone.

## Outgoing video/file: cracked via live packet capture (2026-09-20)

Follow-up to the above -- the user offered to actually do the packet
capture. Turned out not to need a proxy/mobile setup at all: the user's
Chrome browser was already connected to this session's browser-control
tool, and GroupMe has a full web client (web.groupme.com) that supports
both attachment types, so the capture happened entirely through that: a
content script injected into the tab to hook `window.fetch` and
`XMLHttpRequest.prototype.send`/`open` and log every GroupMe-bound
request's URL/method/headers/body shape, then the user sent real test
attachments (a PDF, then a video, then another file) in the account's own
"Test" group while it was watching.

**False starts, in order, because they matter for anyone repeating this:**
- The very first "file" send didn't teach anything new -- it turned out to
  be a re-share of an *existing* file already in the group's history, not
  a fresh upload, so the network calls it produced (`file.groupme.com`'s
  `fileData` metadata lookup, twice) were just the same read path
  `DownloadFile` already used, not an upload at all.
- For a genuinely new video, the capture caught `GET m.groupme.com/uploads`
  immediately followed by `PUT cdn2.groupme.com/uploads/{id}/original.mov`
  with an `x-ms-blob-type: BlockBlob` header (an Azure Blob Storage
  tell) and a heavy SAS query string (`sig`, `se`, `sp`, etc). Tried
  replicating the GET directly (from the same page, then externally) --
  consistently 405'd no matter what headers/methods were tried. Also tried
  skipping straight to a direct PUT with just GroupMe's own
  `X-Access-Token` (no SAS) against a brand-new random ID -- Azure itself
  correctly rejected this with a real `PublicAccessNotPermitted` error
  (harmless: nothing was created, this just confirms Azure Blob Storage
  doesn't accept unsigned writes, full stop). At this point it looked like
  the actual SAS-issuing call must be happening through something
  invisible to page-level JS interception (a Service Worker was the
  leading theory -- both video and, on a later retest, file's real upload
  call showed the same pattern: a multi-minute gap in the capture right
  where the byte-upload must be happening).
- Nearly gave up on both as "needs manual DevTools inspection or deeper
  reverse engineering of GroupMe's client bundle" -- the latter felt like
  it crossed from observing traffic (comparable to everything else
  reverse-engineered this session: reactions, polls) into actively
  decompiling their client code, which their API ToS explicitly
  prohibits, and wasn't worth doing without the user's explicit sign-off.
- The breakthrough was re-examining `file.groupme.com` specifically:
  unlike `m.groupme.com`/`cdn2.groupme.com` (confirmed live to be a real
  Azure Blob Storage passthrough, SAS-token gated), `file.groupme.com`'s
  own response headers (`x-gm-service: file-service`,
  `x-gm-service: authproxy-local`, `server: istio-envoy`) show it's
  GroupMe's *own* backend microservice, not a storage passthrough --
  so a plain authenticated write was worth trying directly rather than
  assuming it needed the same SAS dance as video. It did NOT need a SAS
  URL, and confirming that made it worth re-testing the "just guess a
  simple endpoint" approach for `m.groupme.com/uploads` too, this time
  reading its *validation error responses* instead of guessing headers --
  which immediately revealed the real required JSON fields.

**Video** (`groupmeext.UploadVideo`, `pkg/groupmeext/message.go`):
1. `POST https://m.groupme.com/uploads`, JSON body
   `{"FileSize": <bytes>, "SenderId": "<own user ID>", "Extension": "<ext, no dot>", "groupId": "<group ID>"}`
   (or `"recipientId"` instead of `"groupId"` for a DM -- inferred from
   the validation error's own wording, "You must provide either
   recipientId or groupId", not separately tested against a real DM to
   avoid disrupting a real contact's chat), `X-Access-Token` header.
   The exact required fields were read directly off this endpoint's own
   ASP.NET model-validation errors (e.g.
   `{"errors":{"FileSize":["The FileSize field is required."]}}`) by
   sending deliberately-incomplete requests and reading what it
   complained about next, rather than guessing blind.
   Response: `{"uploadUrl": "<SAS PUT URL>", "renderUrl": "<public playback URL>", "thumbnailUrl": "<public thumbnail URL>", "transcriptUrl": null}`.
2. `PUT <uploadUrl>` with the raw video bytes, `Content-Type: <mime>`,
   `x-ms-blob-type: BlockBlob`. No GroupMe auth needed on this request at
   all -- the SAS signature in the URL is Azure's own auth mechanism.
3. Attach as `{"type": "video", "url": renderUrl, "preview_url": thumbnailUrl}`.

Verified fully from Go (not just replaying the browser's calls): created a
real upload session, uploaded a real small test video's bytes, and
confirmed `renderUrl` serves back content byte-for-byte identical to what
was uploaded. This session/upload was never attached to any message, so
it's an orphaned, harmless, invisible record in GroupMe's storage --
same as every other "verify without sending" test this session.

**File** (`groupmeext.UploadFile`, `pkg/groupmeext/message.go`):
1. `POST https://file.groupme.com/v1/{groupID}/files` with the raw file
   bytes as the body, `Content-Type: <mime>`, `X-Access-Token`. No
   pre-declaration step needed (unlike video) -- this is GroupMe's own
   service, not a storage passthrough.
   Response: `{"status_url": "https://file.groupme.com/v1/{groupID}/uploadStatus?job=<id>"}`.
2. Poll `GET <status_url>` (`X-Access-Token`) until
   `{"status": "completed", "file_id": "<id>"}` -- every real upload
   tried (small test files) completed on the very first poll, so
   `UploadFile`'s retry loop (500ms × up to 20 attempts) is untested for
   an upload that actually takes a while.
3. Attach as `{"type": "file", "file_id": "<id>"}`.

**Filename/mime gap: found and fixed (2026-09-20, follow-up).** The user
tried a real outgoing file send after this landed and got exactly the
predicted symptom -- GroupMe showed it with a blank/generic name (visible
to the user as "everything shows up as txt"). Cracked it by testing many
query parameter names directly against the live API (having already ruled
out multipart, headers, and Content-Type in the original investigation
below): `?name=<filename>` on the upload POST is the one that actually
works -- confirmed live (`?name=test.txt` made `file_name` come back
populated where nothing else had). mime_type turned out not to be a
separate field to set at all: GroupMe derives it server-side from
`name`'s extension (confirmed live -- `?name=real.pdf` produced
`mime_type: "application/pdf"` with no mime information passed anywhere
else in the request). `UploadFile` now takes a `filename` parameter and
includes it as that query parameter; `uploadMatrixFile`
(`pkg/connector/handlematrix.go`) passes `content.Body` (the real Matrix
filename) through, where it previously wasn't passed at all.

Re-verified the same way as everything else here: a real PDF uploaded via
the fixed Go code came back with the correct filename, correct
`application/pdf` mime type, and byte-identical content on download.

One residual, apparently-GroupMe-side gap: a plain `.txt` file's mime_type
still comes back empty even with a correct `name=test.txt` and an
explicit `Content-Type: text/plain` header on the request -- GroupMe's own
extension-to-mime lookup table (whatever it is) just doesn't seem to cover
`.txt`, unlike `.pdf` which works cleanly. Not something this bridge can
fix since it's server-side derivation, not something the request
controls; flagging in case a pattern emerges across more extensions later
(only `.pdf` and `.txt` have actually been tried).

The original investigation below (multipart, headers, Content-Type all
failing to set metadata) is kept for the record since it's what narrowed
down what *didn't* work and made testing query parameter names next the
obvious move -- don't repeat those dead ends if picking this up again.

**Original investigation, before the `?name=` fix above**: the file's
`file_name`/`mime_type` came back empty from `DownloadFile`'s own
metadata lookup afterward, no matter what was tried to set them:
- A `multipart/form-data` body with a `Content-Disposition: form-data;
  name="file"; filename="test.txt"` part (curl's `-F`, which should
  produce exactly this) -- the file's *content* didn't even transfer this
  way (`file_size` came back 0), tried over both HTTP/2 and HTTP/1.1. A
  full wire trace (`curl --trace-ascii`) confirmed the multipart body
  curl generated was textbook-correct (proper boundary, proper
  `Content-Disposition`/`Content-Type` per-part headers) -- this endpoint
  appears to simply not parse multipart at all, possibly stripped at an
  ingress/WAF layer (its responses show `server: istio-envoy`).
- The raw-bytes POST (the one that does work for content) plus a
  `Content-Disposition` header on the request itself, several custom
  `X-*-File-Name`-style headers, and `file_name`/`mime_type` query
  parameters (snake_case) on the URL -- content transferred correctly
  every time (`file_size` matched), but the name/mime metadata stayed
  empty every time regardless. `name` (not `file_name`) was the one not
  yet tried at this point -- see above.

**Not exercised with a real Matrix-triggered send: video only.** The file
path *has* now been confirmed via a real Matrix-triggered send (see
above) -- video has not; `uploadMatrixVideo`'s wiring in
`pkg/connector/handlematrix.go` is structurally identical to the
already-implemented outgoing image path (download Matrix media, call the
upload function, attach the result), and the upload API itself (the
novel, risky part) is independently verified live, but a real end-to-end
video send from Matrix hasn't been tried yet.

## Live incident: crash loop on an unhandled push message type (2026-09-20)

While actively testing the above, the user reported messages had stopped
bridging. Investigation (`sudo journalctl -u matrix-mautrix-groupme.service`)
found the bridge process itself was crash-looping -- `systemctl status`
showed `activating (auto-restart)`, and the log had the same panic
repeating on every single restart attempt:

```
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0xb35a84]
...
	/build/thirdparty/groupme-lib/real_time.go:162 +0x224
created by github.com/beeper/groupme-lib.(*PushSubscription).StartListening
```

Root cause: a genuine bug in the pinned upstream library (predates every
change made this session), not anything introduced recently.
`StartListening`'s push-message dispatch loop looked up a handler by
message type in `RealTimeHandlers` and, on a miss, logged "Unable to
handle GroupMe message type" but then **fell through** to call
`handler(r, channel, content)` regardless -- with `handler` left `nil`
from the failed map lookup. Only five message types are ever registered
(`real_time_handler.go`: `direct_message.create`, `line.create`,
`like.create`, `membership.create`, `favorite`) -- GroupMe pushes several
other types over the same channel in ordinary use (typing indicators
being the most common), so this was never a rare edge case once real,
sustained traffic hit it; it just happened not to have been hit yet.
Fixed in `thirdparty/groupme-lib/real_time.go` to `continue` on an
unhandled type instead of falling through to the nil call -- matching
what the surrounding early-exit checks (ping/empty-type/nil-content) were
already clearly trying to do.

Recovery was automatic once fixed and deployed: REST polling (already
running independently, see "REST polling fallback" above) picked up
every message sent during the outage on its next cycle after the fix
landed -- confirmed by checking the 10 messages sent during the outage
window directly against the `message` table, all present with a real
`mxid` within about a minute of redeploying. No manual backfill or
intervention was needed beyond fixing and redeploying the crash itself.

Given the severity (an actual production crash loop, not a theoretical
concern), this was found, fixed, deployed, and committed ahead of
anything else in progress at the time.

## Live incident: avatar flicker/reupload storm (2026-09-20)

Another user-reported live bug, right after the crash-loop above: "it
keeps refreshing profile images and now the numbers are crazy." Confirmed
via logs -- `req_id` (the bridge's own Matrix API request counter, logged
on every request) climbing by 100+ per minute from a continuous stream of
`avatar_url` GET/PUT pairs across many different ghosts, each PUT setting
a genuinely *new* `mxc://` URI rather than staying stable (i.e., not just
noisy logging -- real, repeated re-uploads of media to the homeserver).

Root cause: the opportunistic per-message ghost refresh added earlier
this session (`HandleTextMessage`, `handlegroupme.go`) passed
`msg.AvatarURL` through to `avatarFor()` unconditionally, including when
empty -- and `avatarFor("")` returns a *Remove* avatar (see
`chatinfo.go`). Checked real message history directly against the API:
GroupMe does **not** reliably include `avatar_url` on every message, even
for a sender who has a real profile picture set on their account (a real
account showed empty `avatar_url` on the majority of its own recent
messages, non-empty on others). So a message that happened not to carry
`avatar_url` was erasing the ghost's real avatar every time, which then
got restored by the next message that did carry one (or by an unrelated
`GetChatInfo`/`GetUserInfo` resync) -- only to be erased again by the
next avatar-url-less message. Under any sustained normal chat activity,
this cycled continuously and forever, not as a one-off glitch.

Fixed: `UserInfo.Avatar` is now only set when `msg.AvatarURL` is
non-empty; otherwise left `nil`, which bridgev2's `Ghost.UpdateInfo`
treats as "don't touch the avatar" (it only inspects `Avatar` when
non-nil) rather than "remove it." The name refresh is unaffected --
`msg.Name` doesn't have this sometimes-absent problem, confirmed against
the same message sample (every message had a non-empty `name`).

Checked `chatinfo.go`'s other `avatarFor()` call sites
(`GetChatInfo`/`GetUserInfo`) for the same class of bug: those all use
authoritative full-profile data (`ShowGroup`'s member list,
`IndexAllChats`, `IndexRelations`, `MyUser`), where an empty URL
genuinely does mean "this user has no avatar set" rather than "this
particular API response just didn't include it" -- so those were left
as-is, correctly.

Verified live: deployed, then watched request logs specifically *after*
the expected one-time startup resync (every restart re-syncs every
group's full member list once, which legitimately produces a burst of
avatar GET/PUT calls -- not a bug, and was correctly excluded from the
comparison) -- confirmed zero `avatar_url` activity in a clean 20-second
window post-settle, versus continuous churn beforehand.

Both this and the crash-loop above were found by directly investigating
a live user complaint about broken behavior, not proactive testing --
worth remembering that "confirmed live" earlier in this document only
ever meant "confirmed under the specific conditions tested," not
"exhaustively stress-tested under real sustained traffic." The
opportunistic-refresh feature in particular had looked correct in
isolated testing (a single message, a single ghost) and only broke under
continuous real usage across many senders.

## Live incident: avatar flicker/reupload storm, take two (2026-09-20)

The fix above (only touch the avatar when a message actually has one)
turned out to be necessary but **not sufficient** -- the user reported
the same symptom continuing minutes later, and in a second room, an even
more extreme case: a single DM ghost ("Dr. String") with *thousands* of
accumulated "changed their name"/"changed their profile picture" events
piled up in Element's room member history.

The real, complete root cause: `HandleTextMessage`'s opportunistic ghost
refresh runs for *every* message it's called with, and `poll.go` calls
`HandleTextMessage` for every message in a chat's most recent page on
*every poll tick* (60s by default) -- including messages that were
already bridged ages ago. That's fine for the message-bridging side
effect itself (bridgev2 core dedupes by message ID before doing anything
observable, see "REST polling fallback" above), but the refresh
goroutine ran as a *direct* side effect, before bridgev2 ever got a
chance to dedupe anything -- no equivalent protection existed for it.

Each message stores the sender's name/avatar as a snapshot from whenever
it was actually sent. A chat's most recent ~20 messages can easily span
a real nickname or avatar change that already happened. Confirmed live:
replaying that same page every poll tick flipped an active sender's
ghost back and forth between the old and new name/avatar, once per
message per tick, forever, for as long as the bridge ran -- growing
without bound, not a one-off glitch. The first fix (empty-avatar-URL
check) only prevented one specific *symptom* of this (erasing the
avatar entirely); the underlying replay-driven flip-flopping remained,
now potentially flipping between two different *real* avatar URLs (or
two different real names) instead of real-vs-removed.

**Fix**: a straightforward per-sender cooldown
(`GMClient.ghostRefreshedAt`/`shouldRefreshGhost`, `client.go`) -- the
opportunistic refresh now runs at most once per sender per 10 minutes,
full stop, regardless of how many messages (old or new, live-pushed or
polled) reference them in that window. Deliberately a blunt cap rather
than trying to precisely distinguish "genuinely new" from "replayed"
inside `HandleTextMessage` itself, which would mean duplicating
bridgev2 core's own dedup logic in a second place.

**Verified live under real sustained traffic**, having learned from the
first fix's insufficiently thorough spot-check: after deploying, watched
the two specific ghosts the user had reported by GroupMe ID (75316972
"Dr. String", 52061885 "Tristan Doan") across several poll cycles --
206 genuinely new (non-duplicate) messages from those two senders were
processed in the following ~5 minutes, with zero resulting
avatar/displayname churn. Before this fix, the same two senders were
producing new churn roughly every 60 seconds (matching the poll
interval), indefinitely, regardless of message volume.

**Lesson for next time, stated plainly**: the first fix was declared
"confirmed live" after watching a clean 20-second window right after
deploy. That was true as far as it went, but the actual bug had a
~60-second period tied to the poll interval, and a single spot-check
right after a restart mostly just observes the (unrelated, expected)
startup `ChatResync` burst rather than the poll-driven path that was
still broken. A real fix verification for anything poll-interval-shaped
needs to span multiple poll cycles under real ongoing traffic, not a
single quiet window -- exactly what both fixes in this document now
did, but only the second one was checked that way from the start.

## Live incident: avatar/name flicker, take three -- the actual root cause (2026-09-21)

The user asked to clean up the historical spam left behind by the two
incidents above (thousands of accumulated "changed their name"/"changed
their profile picture" events cluttering room timelines, slowing message
loading in Element). Handled via Matrix redaction -- see "Cleaning up
the historical spam" below -- but while spot-checking the cleanup's
results, one ghost's *current* (not historical) name was found wrong:
`@groupme_81821868:rishi-rai.com` (real name "CJ Ness") was showing as
literally **"GroupMe"** in two rooms. A live journalctl trace pinned
this to a real `PUT .../profile/.../displayname` with body
`{"displayname":"GroupMe"}`, sent moments before by the *still-running,
already-take-two-fixed* bridge -- not a redaction artifact.

Root cause, finally the actual one: `GetChatInfo`/GroupMe's live group
data was never wrong. The *opportunistic per-message ghost refresh*
(the same feature responsible for takes one and two above) was still
capable of overwriting a ghost's real name with garbage, because the
take-two fix (a 10-minute per-sender cooldown) only throttled
*frequency* -- it never addressed *correctness*. Confirmed live: two
real senders (`81821868` "CJ Ness", `70576355` "Rapha MC") each have a
years-old message on record (from 2021/2022) where GroupMe's own API
returns the literal string `"GroupMe"` as that message's recorded
sender name -- apparently a real GroupMe-side data artifact from
whenever those specific messages first went through (a plausible guess:
sent before the account had a nickname set, or via some
integration/webhook path that defaulted to the app's own name). REST
polling replaying that *one* old message was enough to overwrite the
ghost's real name every time the cooldown lapsed -- roughly every 10
minutes, indefinitely, for as long as the bridge ran. 10 confirmed
occurrences across about 3 hours of logs before this fix, isolated to
exactly these 2 of the 13 originally-affected people (everyone else's
message history apparently doesn't contain this specific artifact).

**The actual fix** (`pkg/connector/handlegroupme.go`): stop trying to
distinguish "good" stale data from "bad" stale data (impossible to do
reliably -- take two tried exactly that, by rate-limiting rather than
eliminating, and a bad value still got through). Instead, skip the
refresh entirely unless the message has never been bridged before,
checked via `gc.Main.br.DB.Message.GetAllPartsByID` -- the exact same
existence check bridgev2 core itself uses for its own message dedup
(see "REST polling fallback" above). A message already in the database
is not fresh information about its sender, full stop, regardless of
what its content says -- this eliminates the entire class of bug (any
message, with any content, old or new, corrupted or not, replayed by
polling) rather than the two specific instances discovered so far. The
per-sender cooldown from take two is kept as a secondary guard against
redundant work during a legitimate burst of new messages, but it's no
longer the thing actually preventing corruption.

Verified live: deployed; the deploy's own restart re-ran the bridge's
full initial sync (`sync.go`), which derives every ghost's name from
GroupMe's live current group-member data, not message snapshots --
both previously-corrupted ghosts (CJ Ness, Rapha MC) were already
showing their correct real names again immediately, no manual
intervention needed. Watched logs for 2+ minutes afterward with the
precise query that would have caught a recurrence -- zero hits.
Followed up with a scripted check of every (room, ghost) pair touched
by the redaction cleanup below: 6 of 24 initially looked like
mismatches against an expected-name list, but every one turned out to
be a legitimate per-group nickname difference (GroupMe nicknames are
set per-group, not account-wide -- confirmed directly against
GroupMe's live API, e.g. `81821890` really is "James" in one group and
"James Sutherland" elsewhere) -- not a bug, just an artifact of the
verification script's own oversimplified comparison list. Zero actual
corruption remained anywhere checked.

## Cleaning up the historical spam left behind (2026-09-21)

Once take two's fix was believed complete (it wasn't fully -- see take
three above, found *during* this cleanup), the user asked to clean up
the thousands of accumulated spam events the first two incidents had
already created, since they were slowing down message loading in
Element. Scoped this carefully given it's a real, mostly-irreversible
live-room-state operation:

- **No server-admin access available** (checked: the real account isn't
  a Synapse admin), and the standard "redact as the room owner" path
  didn't work either -- checked the affected rooms' power levels and
  found `redact: 50` required with `users: {}` (nobody, including the
  real account, has an elevated power level in these appservice-created
  rooms). Neither the Synapse admin API nor regular room-owner
  permissions were usable here.
- **The actual approach**: Matrix always allows a user to redact their
  *own* events regardless of power level. Since every spam event was
  sent by the ghost itself (the ghost's own opportunistic-refresh-driven
  `SetDisplayName`/`SetAvatarURL` calls), and the
  bridge's appservice token can act as any ghost in its own namespace
  (`?user_id=@groupme_<id>:rishi-rai.com`, the same mechanism used for
  double-puppeting the real account, just without needing that account
  to be specifically allowlisted), each ghost could redact its own
  historical `m.room.member` events directly -- no privilege escalation,
  no admin grant, nothing touched outside each ghost's own event
  history.
- **Filtered strictly by type and sender** (`/messages` with
  `{"types": ["m.room.member"], "senders": [ghost]}`), and always
  preserved the *current* (latest) state event per ghost per room,
  redacting only the superseded ones -- confirmed live this doesn't
  affect current room state (redacting a non-current state event has no
  effect on what "current state" resolves to) and doesn't touch actual
  chat messages at all (spot-checked real `m.room.message` events from
  the same senders before and after -- untouched).
- **Scale**: first tested on one room with a small batch (5 events),
  verified state integrity and that a real message stayed intact, then
  scaled up. Total across the account: found 13 people with
  significant accumulated spam (well beyond the 2 the user had
  personally noticed), 40 (room, person) combinations across every
  group/DM each of them appears in, **110,469 events redacted total**,
  spread over roughly 2 hours of runtime (4-way parallel batches --
  each room/ghost combination is its own independent script run,
  hundreds to low thousands of individual redaction API calls each).
  Zero failures across all 40 runs.
- Scripts used for this lived at
  `/tmp/.../scratchpad/clean_member_spam.py` and were not committed to
  this repo -- they're a one-off Matrix Client-Server API cleanup tool,
  not part of the bridge itself, and there's no reason to expect this
  exact cleanup will need repeating now that take three's fix addresses
  the actual root cause.

## Log review: four small fixes (2026-09-24)

A pass over 24 hours of live logs turned up 32 warning/error lines. One
cluster wasn't ours; four real bugs were fixed.

**Not ours: GroupMe outage, 22:01-22:10 UTC.** About 20 poll failures with
500/503/408 from api.groupme.com, including one whose body was GroupMe's own
internal `read tcp 127.0.0.1:...:22121: i/o timeout`. Polling kept retrying
and recovered by itself once GroupMe did. Nothing to change.

**1. False "websocket down for 5 minutes" alert on every routine drop.**
`lastConnected` was only set at handshake, so the first disconnect after
hours of healthy uptime measured all that uptime as downtime. The Error line
fired the same millisecond as the disconnect, with `down_for` of 4-8 hours,
4 times a day. That's noise in exactly the alert the health check greps for.
`connectAndRun` now also stamps `markConnected` on the way out (deferred)
after a successful handshake, so the clock starts when the connection
actually drops.

**2. Reconnect backoff never reset.** `Listen`'s backoff doubled on every
return and was never reset. After any early failure streak it sat at the 60s
cap forever, so every later routine drop waited a full minute to reconnect
(every disconnect in the logs showed `retry_in=60000`). `connectAndRun` now
reports whether it handshook, and a connection that really came up resets
the backoff to 1s.

**3. Reactions in DMs always 404'd.** `HandleMatrixReaction` and
`HandleMatrixReactionRemove` passed only the logged-in user's ID as the DM's
conversation ID. GroupMe's DM conversation ID is both user IDs joined by `+`,
numerically smaller first (e.g. `87270184+106452543`). Confirmed against the
`conversation_id` of all 55 DMs from `/v3/chats`, including DMs where the
other user's ID is lower and where it's higher. Added
`DMConversationID` (pkg/connector/id.go). Group reactions were unaffected.
Not re-verified with a live like, to avoid sending a duplicate reaction
notification; the next real DM reaction will confirm it.

**4. Access token leaked into logs.** `doWithAuthToken` puts the token in
the query string, and net/http's `*url.Error` embeds the full URL. So any
transport error (connection reset, timeout) logged the token verbatim; one
did on 2026-09-24. `Client.do` now strips the query from `*url.Error`
before returning it. Verified with a throwaway test against a refused port.
The token still appears in journald lines logged before this fix.

Also seen and left alone: WhatsApp's 4 routine 503 stream-end reconnects
(self-healed), Synapse `task_scheduler` `KeyError` tracebacks (a race
inside Synapse's own scheduler, triggered by the join burst when a bridge
restarts; harmless), and a few `403 not invited` join denials (bridgev2
tries the join, then invites and retries).

## Profiles flip-flopping between groups on every restart (2026-09-25)

After the 2026-09-24 deploy, the restart resync made 138 profile writes
(102 names, 36 avatars) and 38 media uploads in about a minute. None were
needed. There were two causes.

**Per-group nicknames and avatars fighting over one profile.** GroupMe gives
each person a separate nickname and picture in every group. Matrix gives
each person one profile (their ghost), shared by every room. Each group's
resync wrote its own nickname/avatar into that shared profile, so it
flipped on every restart: "AJ" / "AJ Ball", "Baker Long 2" / "Baker Long",
"Nico" / "Nico Costa", avatars swapping between two different per-group
pictures. A blank per-group picture removed the avatar outright. Every flip
posts a "changed their name/profile picture" event into every room that
person is in. bridgev2 v0.31's `ChatMember.Nickname` (real per-room names)
is documented "Not yet used", and member events are only resent when
membership changes. So per-room nicknames aren't available, and the fix is
one consistent identity per person:

- Name: the account-wide name. The group member list has it as `name`,
  next to the per-group `nickname`; I added `Member.Name` to groupme-lib.
  The nickname is only a fallback.
- Avatar: account-level sources (DM chats list, contacts) set it. A group's
  per-group picture only fills in a missing avatar and never replaces or
  removes one. `avatarIfSet` treats a blank URL as "no info" rather than
  "remove".
- The new-message refresh in handlegroupme.go now only fills gaps (a missing
  name, or the raw-ID placeholder, or a missing avatar). Message snapshots
  carry per-group nicknames/pictures too.

Trade-off: a person who is only in groups (never DMed) keeps whichever
per-group picture was seen first until it's cleared. Where someone set a
more descriptive nickname than their account name, Matrix now shows the
account name.

**DM resync set names to the raw ID.** The DM member entry carried no
profile, so bridgev2 fell back to `GetUserInfo`, which only checks personal
contacts and returned the raw numeric ID for anyone not in them. For
example, "Hilton Sampson" became "94228122" until a group resync set it
back. The DM member now carries the name/avatar already looked up from the
chats list. `GetUserInfo` no longer downgrades an existing real name to the
ID.

Deploy result: the first restart switched 363 people from nickname to
account name, once each with no repeats. 304 of them were in "FREE FOOD!"
(4,175 members, 399 with nickname != name).

Second restart: 0 name writes (was 102 before the fix). The remaining
catch-up converged in one pass. 14 "Ghost profile drifted" warnings were
FREE FOOD member events still carrying old nicknames; bridgev2's
`reconcileProfile` re-pushed them, and all 14 were verified afterwards to
match their profile in that room. 4 DM room avatars and 1 ghost avatar were
switched to the account-level picture.

## DM message requests (2026-09-28)

A DM from someone GroupMe doesn't treat as a contact arrives as a "message
request": the chat has `requires_approval: true`. A reply to Tatum Theobald's
request, sent from Matrix on 2026-09-26, didn't go through until the request
was accepted in the GroupMe app. The bridge logs from that night had already
rotated out, so the exact send response is unknown.

No public or community docs cover accepting a request. It was found in
web.groupme.com's own client bundle: `POST
https://api.groupme.com/v3/chats/<conversation_id>/approve` with
`X-Access-Token`. `GET /v3/chats/<conversation_id>` returns the chat with
`requires_approval`. The conversation ID is the "smaller+larger" form with
a literal `+`. `%2B` 404s and the bare other-user ID 400s. Verified live on
an already-accepted chat: approve returns 200 and changes nothing (a
made-up action on the same path 500s). Not yet exercised on a genuinely
pending request, since there wasn't one to test with.

`HandleMatrixMessage` now calls `approveDMRequestIfPending` before every
outgoing DM. That is one GET, plus an approve only if the chat is pending.
Replying is treated as accepting, the same as the app, which also requires
accepting before you can reply. It's best-effort: any failure is logged
and the send goes ahead anyway.

The web client also has `POST /v3/blocks?user=&otherUser=` for the
"Block" button next to "Accept". Not wired up.
