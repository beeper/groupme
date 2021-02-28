# Features & roadmap
* Matrix → GroupMe
  * [ ] Message content
    * [x] Plain text
    * [ ] Formatted messages
    * [ ] Media/files
    * [ ] Replies
  * [ ] Message redactions
  * [ ] Presence - N/A
  * [ ] Typing notifications
  * [ ] Read receipts
  * [ ] Power level
  * [ ] Membership actions
    * [ ] Invite
    * [ ] Join
    * [x] Leave
    * [ ] Kick
  * [ ] Room metadata changes
    * [ ] Name
    * [ ] Avatar<sup>[1]</sup>
    * [ ] Topic
  * [ ] Initial room metadata
* GroupMe → Matrix
  * [ ] Message content
    * [x] Plain text
    * [ ] Formatted messages
    * [ ] Media/files
    * [ ] Location messages
    * [ ] Contact messages
    * [ ] Replies
  * [ ] Chat types
    * [ ] Private chat
    * [x] Group chat
  * [x] Avatars
  * [ ] Presence
  * [ ] Typing notifications
  * [ ] Read receipts
  * [ ] Admin/superadmin status
  * [ ] Membership actions
    * [ ] Invite
    * [ ] Join
    * [ ] Leave
    * [ ] Kick
  * [ ] Group metadata changes
    * [ ] Title
    * [ ] Avatar
    * [ ] Description
  * [x] Initial group metadata
  * [x] User metadata changes
    * [x] Display name
    * [x] Avatar
  * [x] Initial user metadata
    * [x] Display name
    * [x] Avatar
* Misc
  * [x] Automatic portal creation
    * [x] At startup
    * [x] When receiving invite
    * [x] When receiving message
  * [ ] Private chat creation by inviting Matrix puppet of WhatsApp user to new room
  * [ ] Option to use own Matrix account for messages sent from WhatsApp mobile/other web clients
  * [ ] Shared group chat portals

<sup>[1]</sup> May involve reverse-engineering the WhatsApp Web API and/or editing go-whatsapp  
<sup>[2]</sup> May already work  
<sup>[3]</sup> May not be possible  
