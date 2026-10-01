# Matrix GroupMe Go Bridge

A Matrix–GroupMe puppeting bridge, built on [mautrix-go bridgev2](https://github.com/mautrix/go).

**Development status (2026-10-01):** imported from
[realtofuine/groupme revival-2026](https://github.com/realtofuine/groupme/tree/56998b82223b7a4850bf2f5bf9cea607fe713230),
which continues the original Beeper bridge's Git history. This branch adds
Beeper webview login, account-scoped push authentication, cancellable connection
workers, and paginated conversation discovery. These changes have source-level
regression coverage; current-branch GroupMe/Beeper login and delivery still need
live verification. The upstream author's earlier observations below are useful
background, not validation of this candidate.

## Login and local development

In Beeper, choose the **GroupMe** login flow and sign in at `web.groupme.com`.
The client collects the GroupMe session token using bridgev2's webview login
contract. **Google sign-in was rejected by the embedded browser in live testing**
with "This browser or app may not be secure". Use **GroupMe access token** for
Google accounts:

1. Sign in with Google at <https://web.groupme.com/> in regular Chrome.
2. Open DevTools → Application → Local Storage → `https://web.groupme.com`.
3. Copy the `access_token` value.
4. In Beeper's Login options, choose **GroupMe access token** and paste it there.

This imports the GroupMe session without collecting Google credentials. Keep
the token out of logs, issue reports, and chat messages. Developer tokens from
`dev.groupme.com` also work, but its current login form asks for a GroupMe
password. A seamless Google login will require a supported external-browser
handoff; the embedded flow is not sufficient.

Build with `BUILD_TAGS=goolm ./build.sh`, or `docker build --build-arg
COMMIT_HASH=$(git rev-parse HEAD) -t groupme-dev .`. Run source checks with
`go test -race -tags goolm ./...` and `go vet -tags goolm ./...`.

For Beeper, use a separate runtime directory and registration:

```sh
mkdir -m 700 /absolute/path/to/groupme-runtime
bbctl config --type bridgev2 --param pickle_key=generate \
  --output /absolute/path/to/groupme-runtime/config.yaml sh-groupme-dev
chmod 600 /absolute/path/to/groupme-runtime/config.yaml
```

Run the container with that directory mounted at `/data` and numeric `UID`/`GID`
for its owner. A bbctl-generated config already contains registered appservice
credentials; the launcher preserves them and doesn't generate another
registration. Keep the registration and runtime across source rebuilds.
The legacy bridge's database/config migration has not been implemented or
rehearsed; use a dedicated new runtime for development.

## Upstream validation reported by the fork author

- **Login** via GroupMe access token (`dev.groupme.com` → Access Token).
- **Initial sync**: on login, every existing group and DM gets a Matrix
  room immediately (not just ones that happen to receive a new message).
- **Incoming messages**, delivered two ways simultaneously:
  - A **WebSocket** Bayeux connection (`wss://push.groupme.com/faye`) for
    near-real-time push. Previously cycled/reconnected roughly every 45
    seconds due to misreading a normal quiet `/meta/connect` long-poll
    response as a dead connection; fixed (real WebSocket-level ping is now
    the actual liveness check instead) and verified live at 9+ minutes
    continuous, zero reconnects. See NOTES.md "WebSocket reconnect-cycle
    fix" for details. Also previously **crashed the entire bridge
    process** on any push message type without a registered handler
    (e.g. a typing indicator) — a real bug in the pinned upstream library
    that crash-looped in production; fixed to skip unhandled types
    instead of panicking. See NOTES.md "Live incident: crash loop on an
    unhandled push message type."
  - A **REST polling fallback** (60s interval, configurable) that works
    independently of the WebSocket, so message delivery doesn't depend on
    push being healthy at all. This branch retries the WebSocket while REST
    polling remains available; it no longer starts the uncancellable HTTP
    long-polling transport.
    Requests are staggered across the interval with per-chat backoff on
    rate-limit responses — a naive tight-interval/burst version of this
    got the account 429'd in production; see NOTES.md "Health-check/
    alerting system, and a REST polling rate-limiting bug it caught" for
    the full incident and fix.
- **Reactions**, both directions, with the **actual emoji** GroupMe
  supports (❤️ 👍 🤣 🎉 🔥 😮 👀 😭 🥺 🙏 💀 🫶 🤬 💅 🫠) — not just a
  generic heart. This required patching the vendored GroupMe API client
  (see below): the pinned upstream library predates GroupMe's per-emoji
  reactions feature and only exposed the older undifferentiated "like."
  Seeing reactions is confirmed live end-to-end. Adding a reaction from
  Matrix is code-complete and builds, using the same real emoji the user
  picked in their client, but hasn't been separately confirmed live in
  the Matrix→GroupMe direction (only GroupMe→Matrix was).
- **Double puppeting**: your own messages/reactions show up attributed to
  your real Matrix account, not a separate ghost, confirmed live.
- **Member/room names**: resolved from the group's own membership list and
  the account's chat list (not just the personal contacts/"relations"
  list, which — confirmed live — doesn't include everyone you have an
  active chat with). Names and avatars also opportunistically refresh
  from message sender data, which additionally covers people who've since
  left a group. This caused three real live bugs before landing on a
  correct fix, all now resolved and verified under real sustained
  traffic: (1) a message without an avatar URL was erasing the ghost's
  real avatar instead of leaving it alone; (2) REST polling replaying old
  messages every 60s tick could flip a sender's name/avatar back and
  forth indefinitely if their recent message history spanned a real
  change — a per-sender cooldown reduced this but didn't fully fix it;
  (3) even with the cooldown, a message with genuinely corrupted
  GroupMe-side data (two real senders had a years-old message on record
  literally naming its sender "GroupMe") could still periodically
  overwrite a ghost's real name. The actual fix: the refresh now skips
  entirely unless the message has never been bridged before, eliminating
  "replaying old messages can mutate a ghost's profile" as a category of
  bug rather than patching around specific bad values. See NOTES.md
  "Live incident: avatar/name flicker" (three entries — "take one",
  "take two", "take three").
- **Historical spam cleanup**: the bugs above left thousands of
  "changed their name"/"changed their profile picture" events cluttering
  affected rooms' timelines (110,469 across 13 people and every room
  each appeared in, as of 2026-09-21 — slow enough to load that the user
  asked for cleanup). Redacted via each ghost redacting its own
  historical `m.room.member` events (Matrix always allows redacting your
  own events, regardless of room power level — no server-admin access
  was available or needed), preserving each ghost's current state event
  and leaving real chat messages untouched. See NOTES.md "Cleaning up
  the historical spam left behind" for the full approach; the cleanup
  script itself isn't part of this repo (a one-off Matrix API tool, not
  bridge code).
- **Health-check/alerting** (outside this repo, lives on the host at
  `/matrix/health-check/`): a systemd timer every 5 minutes checks that
  all Matrix-related services are up and greps recent logs for
  errors/panics, posting to a dedicated Matrix room if it finds a problem.
  Not bridge-specific, but worth knowing about — it's what caught the
  rate-limiting bug above in the first place. See NOTES.md for details if
  it ever needs adjusting.
- **Sustained websocket-outage alerting**: a single websocket reconnect is
  silent by design (expected, self-heals in ~2s). But if the websocket
  hasn't held a successful handshake in over 5 minutes — meaning
  real-time push is degraded and delivery has fallen back entirely to the
  60s-interval REST poll — the bridge logs an Error-level line that the
  health-check above picks up, repeating every 15 minutes while the
  outage continues. No message loss either way; this is purely about
  visibility into "real-time delivery has been down for a while." See
  NOTES.md "Sustained websocket-outage alerting."
- **Incoming file attachments** (GroupMe → Matrix): confirmed live against
  a real file already in the account's history (downloaded via GroupMe's
  file.groupme.com API and re-uploaded to Matrix's media repo with the
  correct filename/mime type recovered from GroupMe's own metadata).
  Group-only, matching GroupMe's own file-sharing feature — a file
  attachment somehow appearing on a DM is logged and skipped rather than
  guessed at.
- **Polls** (GroupMe → Matrix, read-only): a poll being created shows the
  actual question and options (fetched live from GroupMe's undocumented
  poll API, reverse-engineered against a real account — see NOTES.md), a
  poll ending shows the final vote tally, and the "about to expire"
  reminder is passed through. Deliberately one-way and plain-text, not
  Matrix's native interactive poll widget — see NOTES.md "GroupMe polls"
  for why, and for the fact that video/location remain unverified but
  polls' rendering *is* confirmed against real data (a live account poll
  plus historical finished-poll data, run through the conversion code
  directly without touching Matrix).

## Implemented but not yet verified live

- **Outgoing images** (Matrix → GroupMe): downloads the Matrix media
  (handles both encrypted and unencrypted rooms), uploads it to GroupMe's
  separate image-upload host (`thirdparty/groupme-lib/image_service.go`,
  a local addition — GroupMe's image service was never wired up in this
  library at all), and attaches the result to the outgoing message. Builds
  and deploys cleanly. **Deliberately not exercised with a real send** as
  of this writing (the session that wrote it was explicitly asked to
  implement without triggering a real message to real contacts) — next
  session with the ability to send a real test image should verify this
  end-to-end before trusting it.
- **Outgoing locations** (Matrix → GroupMe): no upload/API call needed
  (unlike image) — just parses the outgoing event's geo URI into GroupMe's
  location attachment shape. The parsing itself is confirmed correct
  (a throwaway unit test covered a plain coordinate pair, one with an
  accuracy suffix, one with an altitude component, and a malformed input),
  but — same caveat as outgoing images — **not exercised with a real
  send**, since that would mean sending a real message.
- **Outgoing video and file** (Matrix → GroupMe): GroupMe doesn't publicly
  document an upload endpoint for either, so both were reverse-engineered
  live — a packet-capture session against the real GroupMe web client
  (sending real test attachments in the "Test" group), then independently
  confirmed from this Go code directly against the live API, uploading a
  real (orphaned, never attached to any message) test video and file and
  reading each one back byte-for-byte identical. See NOTES.md "Outgoing
  video/file attachments" for the full investigation.
  - **Video**: fully confirmed — GroupMe hands back a one-time SAS-signed
    Azure Blob Storage URL to upload to, then a permanent public URL to
    reference. Verified end-to-end from Go; not yet confirmed via a real
    Matrix-triggered send.
  - **File**: **confirmed via a real Matrix-triggered send** — the first
    version landed with no filename/mime type (GroupMe showed it
    blank/generic), which turned out to need the filename passed as a
    `?name=` query parameter specifically (nothing else tried had any
    effect); GroupMe then derives the mime type itself from that
    filename's extension server-side. Fixed and re-verified (a real PDF
    upload came back with the correct filename, correct
    `application/pdf` mime type, and byte-identical content). One
    residual gap: GroupMe's own extension-to-mime lookup doesn't seem to
    cover every extension (a plain `.txt` file came back with an empty
    mime type despite a correct filename) -- common types like PDF are
    confirmed fine, but an obscure extension might still show up mime-less
    on GroupMe's side; nothing this bridge can do about that specifically
    since it's GroupMe's own table, not something under this bridge's
    control.
    pattern as the already-implemented image path.
- **Incoming video and location attachments** (GroupMe → Matrix): ported
  from the pre-2023 bridge and builds cleanly, but no example of either
  existed in the account's scanned history to verify against live (unlike
  file attachments, above, which were). Location is pure
  parsing/formatting with no network call, so the risk there is low;
  video downloads via a cookie-authenticated request to GroupMe's video
  CDN that hasn't been exercised against the real service at all. Worth
  confirming the next time either type actually shows up.

## Known gaps

- **Missed-message recovery is incomplete.** Polling still fetches only the
  latest 20 messages per chat, so larger gaps can lose messages.
- **No message history backfill.** New portals only show new activity
  going forward, plus an incidental one-page (~20 messages) dump from
  whatever the first poll happens to return — not a real backfill.
- **DM portal-key heuristic on the live-push path is unverified.** GroupMe's
  DM `conversation_id` is actually a compound `"<id1>+<id2>"` string; the
  current live-push handler's heuristic for picking the DM portal doesn't
  replicate the old bridge's proper parsing of that. The *initial sync*
  path doesn't have this problem (it gets the other user's ID directly from
  the chat list), so this is a narrower edge case than it sounds — but
  worth fixing if a DM ever routes to the wrong room.
- **Custom per-group "like icon" isn't read.** If a group has swapped its
  default like icon for something outside GroupMe's standard 15-emoji set,
  reactions in that specific group will fall back to ❤️ instead of the
  real custom icon.
- bridgev2 supplies provisioning endpoints. Provisioning through the Beeper
  client, space-room support, and the full feature set still need live testing.

## Architecture quick reference

- Go, bridgev2 (`maunium.net/go/mautrix/bridgev2`), same framework as
  `mautrix/whatsapp`, `mautrix/gmessages`, etc. — those are good reference
  implementations if you need a pattern for something not yet ported here.
- `pkg/connector/` is the bridge implementation. `pkg/groupmeext/` wraps
  the GroupMe API client and push transports.
- **Two local vendor patches**, both via `go.mod` `replace` directives
  (real upstream forks weren't possible to publish from the environment
  that wrote them — consider upstreaming for real if these prove out):
  - `thirdparty/wray/` — retained from the imported fork, currently unused by
    the bridge. It patches `github.com/karmanyaahm/wray` (the HTTP
    long-polling Bayeux client) to force HTTP/1.1, working around a
    hang/504 against `push.groupme.com` over HTTP/2.
  - `thirdparty/groupme-lib/` — patches `github.com/beeper/groupme-lib`
    (archived upstream, last commit Oct 2022) to add `Message.Reactions`
    (GroupMe's newer per-emoji reaction data, absent from the pinned
    version) and give `CreateLike` an emoji parameter.
- Config generation, registration, and Docker build follow standard
  bridgev2 conventions: `./build.sh`, `-e`/`-g` flags, `Dockerfile` +
  `docker-run.sh`. See [NOTES.md](./NOTES.md) for the exact deployment
  pattern used in the live setup this was verified against (custom systemd
  unit + isolated SQLite DB, since there's no upstream Ansible role for
  this bridge).

## For the next person/session picking this up

1. Read [NOTES.md](./NOTES.md) for the detailed "why" behind each fix
   above — it's a chronological log, most useful for understanding a
   specific piece rather than as a first read.
2. The "Known gaps" list above is the best starting point for what to work
   on next. The DM portal-key heuristic and custom like-icon items are the
   most likely to actually bite someone.
3. If you find a new bug, fix it, verify it live if you can, and update
   both this file's status sections and NOTES.md — that's what made this
   revival tractable across multiple sessions instead of everyone
   re-discovering the same things.

## Discussion
Matrix room: [#groupme-go-bridge:malhotra.cc](https://matrix.to/#/#groupme-go-bridge:malhotra.cc)

## Credits

Forked from https://github.com/karmanyaahm/matrix-groupme-go which was archived.
Revived and ported to bridgev2 starting 2026-09.
