# Roadmap

| Feature | GroupMe → Beeper | Beeper → GroupMe |
|---|---|---|
| Group and DM text | ✅ | ✅ |
| Formatting | Plain text | Flattened to plain text |
| Replies | ✅ | ✅ |
| Images and GIFs | ✅ | ✅ |
| Video | ✅ | ⚠️ Uploads, but GroupMe can't play it back |
| Voice notes | ✅ | ✅ |
| Files | ✅ Groups only | ✅ Groups only |
| Media captions | ✅ | ✅ |
| Locations | ✅ | ✅ |
| Direct media | ✅ Images, videos, voice notes, group files and avatars | N/A |
| Emoji reactions | ⚠️ Push events, reconciled on reconnect | ✅ |
| Custom reaction icons | ❌ | ❌ |
| Polls | ✅ Active polls; reminders and results as text | ⚠️ Voting only; no creation or vote withdrawal |
| Group name, description and avatar | ✅ | ❌ |
| Group membership | ✅ | ❌ |
| User profiles | ✅ | ❌ |
| History backfill and catch-up | ✅ | N/A |
| Contacts and new DMs | ✅ | ✅ Known contacts |
| DM message requests | ✅ | ✅ |
| Edits | ✅ Live and recent reconnect updates | ✅ Groups and DMs; 15-minute window; text/captions only |
| Individual message deletion | ✅ | ✅ |
| Typing notifications | ✅ Groups and DMs | ⚠️ Start pulses; SDK does not refresh sustained typing |
| Read receipts | ✅ Own checkpoints and DM seen | ✅ Public reads and DM seen; private reads need SDK support |
| Whole DM conversation deletion | ✅ Deletion and reappearance | ✅ Deletion and reappearance |

Groups have no delete-for-self operation equivalent to deleting a DM
conversation.
