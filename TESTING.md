# Manual Beeper acceptance tests

These checks are intended for a Beeper user running the GroupMe bridge through the normal appservice/Hungryserv path. They focus on observable bridge behavior rather than implementation details.

## Prerequisites

- A Beeper account connected to the GroupMe bridge.
- At least one GroupMe group and one GroupMe direct message that are visible in Beeper.
- A way to stop and start the bridge process for downtime/restart tests.
- For high-volume tests, use a disposable GroupMe group so other participants are not spammed.

When checking timestamps, compare the time shown in Beeper with the original GroupMe message time, not the bridge restart time.

## 1. Existing group: live inbound message

1. Leave the bridge running.
2. Send a text message from GroupMe in an already bridged group.
3. Wait for the REST polling interval.

Expected:

- The message appears once in the corresponding Beeper room.
- Sender and text match GroupMe.
- The Beeper timestamp matches the GroupMe post time.
- No duplicate appears on the next poll.

## 2. Existing DM: live inbound message

1. Leave the bridge running.
2. Send a direct message to the connected GroupMe account.
3. Wait for the REST polling interval.

Expected:

- The DM appears once in the existing Beeper DM.
- Sender, text, and timestamp match GroupMe.

## 3. Short downtime catch-up

1. Confirm a group is fully caught up in Beeper.
2. Stop the bridge.
3. Send several GroupMe messages while the bridge is stopped.
4. Start the bridge again.

Expected:

- Every message sent during downtime appears in Beeper.
- Messages are delivered oldest-to-newest.
- Each message keeps its original GroupMe timestamp.
- Restarting the bridge again does not duplicate them.

Repeat the same test with a GroupMe DM.

## 4. Multi-page downtime catch-up

Use a disposable group.

1. Confirm the room is caught up and stop the bridge.
2. Create more than 100 GroupMe messages while it is stopped.
3. Start the bridge.

Expected:

- Catch-up continues across multiple GroupMe history pages until it reaches a message already stored by the bridge.
- All missing non-system messages appear once.
- Ordering and original GroupMe timestamps are preserved.
- There is no fixed page-count cutoff in normal catch-up.

## 5. Backlog larger than the message buffer

Use a disposable group.

1. Stop the bridge after the room has a durable message baseline.
2. Create a backlog larger than the configured portal message buffer.
3. Start the bridge and watch its logs.

Expected:

- The bridge may log that it is applying lossless backpressure.
- It must not log `Buffer is full, dropping message`.
- After catch-up finishes, the number of new Beeper messages matches the GroupMe backlog, excluding GroupMe system events.
- A second restart does not create duplicates.

## 6. Restart with no new messages

1. Start the bridge and wait until polling is stable.
2. Stop it without sending anything new.
3. Start it again.

Expected:

- Existing messages are not replayed.
- No duplicate Beeper messages are created.
- Existing portal-to-room mappings stay unchanged.

## 7. Newly created GroupMe group

1. Leave the bridge running.
2. Create a new GroupMe group.
3. Send a message in it.

Expected:

- The bridge discovers the new conversation.
- A Beeper room is created when the conversation becomes eligible for delivery/sync.
- The current recent message(s) arrive without replaying the entire old history of an unrelated room.
- The room appears in the normal Beeper conversation list rather than Requests when Hungryserv auto-join is available.

## 8. Unsynced group discovery does not create phantom portals

This check is easiest with more GroupMe groups than the configured initial conversation sync limit.

1. Start the bridge.
2. Let it discover the GroupMe account's active groups.
3. Do not interact with groups that are outside the configured initial sync set.

Expected:

- Merely discovering an unsynced group does not create a blank Matrix room.
- Existing mapped rooms remain mapped.
- A previously unsynced group is materialized only when it is actually selected for sync or receives a message that must be bridged.

Operator check: the portal table should not accumulate blank/unmapped group rows after repeated restarts.

## 9. Group avatar stability

1. Pick a bridged GroupMe group with an avatar.
2. Start/restart the bridge several times without changing the GroupMe avatar.
3. Observe the Beeper room avatar.
4. Change the GroupMe avatar once and allow metadata sync to run.
5. Optionally remove the GroupMe avatar and allow metadata sync to run.

Expected:

- Repeated syncs do not repeatedly replace an unchanged avatar when the GroupMe URL differs only by query string or fragment.
- A real avatar change updates the Beeper room avatar.
- Avatar removal clears the Beeper room avatar.
- The avatar is applied to the room, not the puppet/bot profile.

## 10. Outbound Beeper message regression check

1. Send a text message from Beeper into a bridged GroupMe group.
2. Send another into a bridged DM.

Expected:

- Each message arrives in GroupMe once.
- Inbound polling does not echo the sent message back as a duplicate.
- Subsequent restart/catch-up does not duplicate it.

## 11. Media regression check

For an existing bridged group, send one supported media message from GroupMe, such as an image.

Expected:

- The media arrives in Beeper using the same sender/room mapping as text messages.
- If the message also contains text, the text remains associated with the original GroupMe timestamp.

## 12. Portal metadata regression check

Change a GroupMe group's name or topic, then allow portal sync to run.

Expected:

- The Beeper room metadata updates.
- The bridge does not create a second room for the same GroupMe group.
- Existing message history and mapping are preserved.

## Useful log checks

During normal operation and catch-up:

- `REST reconcile group ... bridging N missing messages` or the corresponding DM line indicates durable catch-up.
- `Message buffer is full; applying lossless backpressure instead of dropping messages` is acceptable during a large backlog.
- `Buffer is full, dropping message` must never appear.
- Repeated restarts with no new GroupMe traffic should not produce repeated catch-up of the same messages.
