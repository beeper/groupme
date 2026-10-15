# Matrix–GroupMe bridge

A GroupMe puppeting bridge using [mautrix-go bridgev2](https://github.com/mautrix/go).

This is a development bridge. The October 14, 2026 candidate builds and passes
native request, conversion, history, and activity-routing checks. A dedicated
self-hosted connection on a production Beeper account has verified group/DM text,
replies, reactions, and initial history with two GroupMe accounts. Media validation
is incomplete.

## Supported connector behavior

- Token and Beeper webview login, with account-scoped group and DM portals.
- Group and DM discovery, contact listing, new DMs with known contacts, text,
  replies, and native emoji reactions.
- Images, GIF images, videos, locations, and group file attachments. Generic files
  in DMs are rejected because GroupMe's file service is group-scoped.
- Media captions and filenames; formatted text is flattened to plain text.
- Optional framework direct media for images, videos, group files, and avatars.
- Incoming group metadata, membership, and account profiles.
- DM message requests through bridgev2's standard acceptance interface.
- Incoming active polls with single- and multiple-choice voting from Beeper,
  both verified live. Poll reminders and final results are rendered as text.
- Initial history, older-message pagination, and catch-up of messages missed while
  disconnected, through bridgev2's standard backfill interface. Historical
  reactions and replies are included in converted history.

[ROADMAP.md](ROADMAP.md) lists functionality in each direction.
Individual message deletion, poll creation, vote withdrawal, live vote-result
synchronization, and outgoing group management are not advertised as supported.

## Login

In Beeper, choose **GroupMe** and sign in at `web.groupme.com`. The client collects
the GroupMe session using bridgev2's webview login contract. Google sign-in was
rejected by the embedded browser in earlier live testing; use **GroupMe access
token** in that case:

1. Sign in at <https://web.groupme.com/> in regular Chrome.
2. Open DevTools → Application → Local Storage → `https://web.groupme.com`.
3. Copy `access_token` into the GroupMe access-token login field in Beeper.

Developer tokens from `dev.groupme.com` also work. Keep credentials out of logs,
chat messages, and source control. The bridge stores its session in the normal
bridgev2 login metadata; protect the runtime directory and database accordingly.

## History and delivery

The connector supplies native IDs, messages, reactions, and pagination cursors.
Bridgev2 owns local delivery, deduplication, mapping persistence, backfill queues,
and history limits. Enable and configure the standard `backfill` settings in the
generated config (`backfill.enabled` defaults to false), including
`max_initial_messages` and `max_catchup_messages`. GroupMe group pages contain
up to 100 messages; DM pages contain 20.

Live messages arrive only through GroupMe's push connection; there is no REST
polling. The bridge does not wait for the framework to notice a gap. Each time
the push user channel subscribes, at startup and after every reconnect, the
connector lists groups and DMs and queues a `ChatResync`. Its backfill check
compares GroupMe's newest message ID with the newest bridged message. Pushes from
that connection are held until those resyncs are queued, so a live message
cannot become the newest bridged message ahead of the gap behind it. Bridgev2 then
fetches up to `max_catchup_messages` through `FetchMessages`. With backfill
disabled the bridge logs a warning at startup, and messages missed while
disconnected are not bridged.

Live testing on October 9 confirmed that the user channel delivers `like.create`
and `like.delete` for reactions to the account's own messages. These are bridged
as individual users' reaction changes, including the native emoji. A separate
group subscription received typing events but no reaction changes to another
account's messages in the tested group. Direct capture of both native Web tabs
also observed no such reaction push, despite working group typing and
user-channel reactions.
The `favorite` decoder accepts Web's group and DM envelopes and their full
reaction snapshots, including empty snapshots for removals. The bridge subscribes
to every discovered group and DM, including chats discovered through new messages
or group joins. It does not depend on chat-view notifications, which the standard
appservice backend never supplies.

At startup and on push reconnection, the bridge also reconciles reactions from
one recent native message page per chat (up to 100 group messages or 20 DMs),
even when no newer message exists. This is a connection-triggered refresh, not
periodic polling. Reactions outside those pages arrive through history or native
push events. Full live coverage is still limited by which events GroupMe emits;
the tested self-reaction on the other account's message was also stale in Web
until reloading.

Bridgev2 uses timestamp cutoffs even when distinct native IDs share a timestamp,
and GroupMe timestamps have second precision. Messages that share the history
boundary's timestamp are therefore not guaranteed to be filled.

GroupMe's `source_guid` only deduplicates sends within a short window. Successful
synchronous sends return their native message ID to bridgev2. This is not a promise
of exactly-once delivery across a crash after GroupMe accepts a message.

The previous connector-specific poll tables are no longer read or written, and the
`network.poll` settings have been removed. Existing login and framework
message/portal mappings are preserved. Queued payloads from the old poller are not
replayed; native history is the source for subsequent framework backfill. Back up
existing state before changing versions. The October 9 push-only candidate was
tested on the preserved dedicated self-hosted registration and authenticated
runtime; broader production rollout remains unverified.

## Edits, typing, receipts, and chat deletion

The connector implements group and DM edits in both directions, with GroupMe's
15-minute edit window and 1000-character text limit. Native update events edit
the original Matrix message; native history's separate edit notifications are
not imported as new messages. Hidden edit records advance history pagination
without consuming the requested visible-message count. Outbound edits retain
native media and reply attachments; replacing media is rejected. Text-relative
native mention and emoji offsets are removed when editing plaintext. Recent edit notifications are replayed
alongside the existing reaction refresh after reconnect (100 group messages or
20 DM messages); this is not unlimited recovery of old edits.

Group and DM typing use GroupMe's five-second native pulses. Incoming pulses
expire normally. The pinned standalone bridgev2 version only forwards outgoing
start/stop transitions, so sustained typing may disappear after five seconds.

Public Matrix read receipts update the native account's unread checkpoint;
explicit DM reads also publish the native peer-visible seen receipt. GroupMe
only exposes peer seen receipts in DMs. Native account checkpoints and DM seen
receipts are bridged back to Matrix, including cursors pointing at hidden edit
notifications. Private Matrix receipts are not forwarded by the pinned SDK.

DM conversation deletion uses the account-local `Delete chat` operation, separate
from clearing history or blocking the other user. Native `chat.deleted` and group
`membership.destroy` events remove the account-scoped portal. Groups do not offer
the same delete-for-self operation; leaving or ending a group must be done in
GroupMe. Delete-for-everyone is rejected. The DM delete capability is enabled;
an incoming message after deletion restores the chat as a new Matrix room.

The October 13 candidate ran directly as a local binary on a dedicated self-hosted
connection. Live native-to-Matrix text edits and typing were observed in
both group and DM chats; DM own/peer receipts also passed after editing. Beeper
group and DM edits and DM typing were received by the native peer. DM deletion
was verified in both directions: the deleting account's chat disappeared,
the peer retained its history, and a new peer message restored the chat and
created a fresh Matrix room. An isolated Beeper read also produced the native
peer-visible DM seen receipt, with both native sessions of the reading account
kept away from the DM. These are local validation results, not production
readiness claims.

## Blocking

DMs support blocking and unblocking the other user through mautrix's
`com.beeper.block_user` event and `block-user` / `unblock-user` commands.
The connector uses GroupMe's account-level block operation, which prevents the
blocked person from sending DMs or adding the blocker to groups. Blocking does
not leave shared groups or delete conversation history. Spam reporting is not
implemented and requests for it are rejected before changing the block state.

DM metadata refreshes, including reconnect sync, read the account's outgoing
block list. GroupMe's `/blocks/between` endpoint is symmetric and is deliberately
not used for this: a block by the other person is not a block you can undo.
Failed lookups preserve the previous state. No native block-change push was
observed, so changes made in GroupMe are picked up on metadata refresh or
reconnect, rather than immediately.

The October 15 implementation was checked against both native tester accounts:
the compiled connector's block/unblock calls, repeated calls, and directional
DM metadata. Browser requests were captured through Chrome remote debugging.
This validates the connector/native API path; Beeper UI and deployment validation
remain separate.

## Media limits

The connector retains its 25 MiB image/video send limit and supports the
[documented 50 MB document limit](https://support.microsoft.com/en-us/groupme/how-do-i-share-a-document-in-groupme).
It checks HTTP status codes, bounds downloads at 50 MB, and avoids logging signed
upload URLs. Download failures
produce a visible attachment notice while retaining message text when reuploading; Matrix upload
failures are returned to bridgev2. A failed native attachment download is not
silently represented as a successful empty message.

## Direct media

The connector implements bridgev2's standard direct-media interface. Enable
`direct_media.enabled` in the generated config and configure `server_name` with
a media domain routed to the bridge, using the framework's normal federation
delegation or reverse proxy setup. Keep the generated `server_key` across updates.
No additional GroupMe configuration or database is required.

With direct media enabled, incoming images, videos, group files, and avatars use
framework-generated MXC URIs. The bridge downloads bytes from GroupMe when the
media is requested, retaining its HTTP status and size checks. Group files fetch
their filename, MIME type, and size during conversion. Video and file IDs refer
to the originating login; its current token is used at download time and is not
included in the MXC URI. Download failures are returned by the media endpoint.
Image/video URLs are reused as received; the connector does not refresh them on
403/404. The observed attachment URLs have no expiry parameters, which does not
guarantee permanent availability. Group files are looked up by ID on every fetch.
[GroupMe documents media retention limits](https://support.microsoft.com/en-US/GroupMe/how-long-are-files-and-data-available-in-groupme);
refreshing a URL cannot recover a deleted or expired underlying file.

Direct media defaults to disabled, preserving ordinary Matrix reuploads. Existing
messages keep their original media references. Media served on demand depends on
the native file remaining available and, for video/files, the login remaining
present. The October 8 direct-media implementation has focused local checks;
live verification still requires a routed media domain. It does not resolve the
separate native video-playback issue.

## Build and local development

```sh
BUILD_TAGS=goolm ./build.sh
go vet -tags goolm ./...
go test -race -tags goolm ./pkg/connector ./pkg/groupmeext
```

Tests cover native request/response handling, GroupMe conversion rules, history
pagination across hidden edits, activity routing, and receipt target resolution.
Full framework delivery, capability enforcement, media routing, and chat lifecycle
tests are outside this connector's test scope.

Use libolm instead of `goolm` for the shipped container:

```sh
docker build --build-arg COMMIT_HASH="$(git rev-parse HEAD)" -t groupme-dev .
```

Use an isolated, private runtime for Beeper self-hosting, and preserve its
registration, database, and credentials across source rebuilds. The launcher runs
from `/data`, uses numeric `UID`/`GID`, and accepts an existing bbctl-generated
config without generating another registration. Do not reuse legacy bridge state
with a new registration. Migration from the pre-bridgev2 implementation has not
been implemented.

## Architecture and credits

`pkg/connector` implements bridgev2 interfaces. `pkg/groupmeext` supplies GroupMe
media and WebSocket/Bayeux transport helpers. The GroupMe API client is
[beeper/groupme-lib](https://github.com/beeper/groupme-lib).

Imported from [realtofuine/groupme revival-2026](https://github.com/realtofuine/groupme/tree/56998b82223b7a4850bf2f5bf9cea607fe713230),
continuing the original Beeper bridge and
[karmanyaahm/matrix-groupme-go](https://github.com/karmanyaahm/matrix-groupme-go).
