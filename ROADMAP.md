# GroupMe bridge status

The table describes connector implementation. Live evidence for the October 8,
2026 candidate is listed separately below.

| Capability | GroupMe → Beeper | Beeper → GroupMe |
|---|---|---|
| Group and DM text | Implemented | Implemented |
| Formatting | Plaintext | Flattened to plaintext |
| Replies | Implemented | Implemented |
| Images / GIF images | Implemented | Implemented |
| Video | Implemented; current live proof pending | Uploaded bytes verified; native playback fails |
| Generic files | Implemented for groups | Implemented for groups |
| Media captions | Implemented | Implemented |
| Framework direct media | Implemented for images, videos, group files, and avatars; live routing pending | N/A |
| Locations | Implemented | Implemented |
| Standard emoji reactions | Partial: native push events plus recent reaction reconciliation on reconnect | Implemented |
| Custom group reaction icons | Not implemented | Not implemented |
| Polls | Interactive active polls; reminders/results as text | Single- and multiple-choice voting verified; creation not implemented |
| Group name, description, avatar, membership | Resynced | Not implemented |
| Profiles | Account-level name/avatar, message snapshot fallback | Not implemented |
| History | Standard bridgev2 initial and backward interfaces; catch-up on every push (re)subscription | N/A |
| Contacts and starting DMs from Beeper | Contact list | Implemented for known contacts |
| DM message requests | Native pending status | Standard framework acceptance hook; live validation pending |
| Edits and message deletion | Not implemented | Not implemented |
| Typing and read receipts | Not implemented | Not implemented |
| Conversation deletion | Not implemented | Not implemented |

## Remaining validation and implementation

- Direct media uses the existing bridgev2 interface and download routes. Local
  native-response checks cover credentials, bytes, filenames, HTTP failures,
  malformed metadata, and size limits. Framework media routing is not mocked
  or tested by the connector. The running self-hosted config still has it disabled and uses a
  placeholder media domain. A reachable media route and candidate deployment
  are required for live verification; the production poll passes below do not
  establish direct-media behavior.

- Verified on the dedicated production-account self-hosted registration: group
  and DM text in both directions, replies with the correct parent in both
  directions, native emoji reactions in both directions, and the two pre-login
  history markers. Native incoming reactions were observed through the since-removed
  REST poller; removal also produced a successful Matrix redaction of the original
  reaction. October 9 testing confirmed modern `like.create`/`like.delete`
  events on the user channel for own-authored messages. Group and DM additions,
  emoji changes, and removals reached Matrix without polling; the operator also
  confirmed group removal and DM addition render in Beeper. A separately acknowledged
  group subscription received typing events but no reactions to the other
  account's messages. Direct Chrome capture reproduced that absence in native
  Web, with group typing and own-message reactions as positive controls. The `favorite`
  decoder now supports Web's full group/DM reaction snapshots; live delivery of
  those events remains unresolved. Full push-only reaction coverage is unverified.
  The bridge now subscribes to all discovered group and DM channels, without
  depending on the appservice's unavailable chat-view callback. It refreshes one
  recent native message page per chat on connection recovery to reconcile missed
  reactions independently of new-message backfill.
- Catch-up after a push reconnect, and holding live pushes until it is queued,
  are untested against GroupMe. Verify by dropping the push connection, sending
  native messages, and confirming they are bridged in order after reconnecting.
- The initial import delivered 17 group messages and 2 DM messages. This is
  evidence for that measured history, not an unlimited-history guarantee.
- A native PDF with a caption reached Beeper and the operator confirmed it opens.
  The returned PDF is visible in the native counterparty's group with its filename
  preserved. The forwarded copy has no caption; outgoing caption preservation and
  a byte comparison remain unverified.
- Native web image uploads timed out, video upload stalled, and the built-in GIF
  picker also failed before creating a message. Those attempts do not validate the
  bridge's live upload paths. The retained captioned image was accepted by Matrix
  during the initial import, and the operator confirmed that both image and caption
  render in Beeper after navigating to its October 1 history event.
- Forwarding that image from Beeper delivered a fully loaded image in the native
  counterparty's group. Beeper emitted its caption as a separate Matrix event;
  both native messages arrived. The operator also found Nick Barrett through New
  Chat and opened the existing DM. No provisioning request appeared in bridge logs
  for that lookup, so this verifies client lookup, not a fresh contact-list fetch
  or new-conversation creation.
- A Beeper video send created the native video message. Downloading its render
  URL returned the exact 2,165-byte MP4 fixture, including a successful HTTP 206
  range response. GroupMe's web player still reports that the video cannot play.
  Its DOM uses the returned render URL and has no cross-origin attribute. Browser
  control permission denied a direct navigation to the media host. The operator
  opened the same URL manually in Chrome and reported that it hangs without
  loading. The failure therefore also occurs outside GroupMe's player, despite
  the earlier successful VM download. This is not a video end-to-end pass.
- Single- and multiple-choice poll voting are verified through Beeper and the
  bridged account's native GroupMe client. The native client confirms Option B on
  the single-choice poll and both options on the multiple-choice poll. The
  connector uses MSC3381 events, partial group poll capability, native option IDs,
  and the existing framework's synthetic vote mappings. Multi-choice requests
  use the native path without a trailing slash; the published community example's
  trailing slash caused HTTP 500. Focused native-response checks pass.
  Poll creation, vote withdrawal, and live result synchronization are not implemented.
- The simplification candidate moves pending DM acceptance to bridgev2's
  standard hook, removes duplicate framework handling, and shares media transfer
  paths. It has not been deployed; the live evidence above predates this cleanup.
- Measure current native message-edit, delete, typing, read-receipt, custom-icon,
  and conversation-management contracts before implementing or advertising them.
- Exercise contact discovery and new-DM creation through the framework's standard
  provisioning interfaces.
- Complete video, location, and single-event outgoing-caption checks through the
  real clients. Incoming retained image/caption and group PDF opening are confirmed;
  image and PDF return messages are verified in the native client.
- Document and resolve the framework's equal-timestamp history cutoff upstream.
  Keep connector delivery and persistence on the standard framework paths.

Framework and lifecycle test suites are outside the current work scope. Retain
focused tests of GroupMe requests, response validation, and conversion behavior.
