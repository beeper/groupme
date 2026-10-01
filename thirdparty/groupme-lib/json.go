// Package groupme defines a client capable of executing API commands for the GroupMe chat service
package groupme

import (
	"encoding/json"
	"fmt"
)

// Meta is the error type returned in the GroupMe response.
// Meant for clients that can't read HTTP status codes
type Meta struct {
	Code   HTTPStatusCode `json:"code,omitempty"`
	Errors []string       `json:"errors,omitempty"`
}

// Error returns the code and the error list as a string.
// Satisfies the error interface
func (m Meta) Error() string {
	return fmt.Sprintf("Error Code %d: %v", m.Code, m.Errors)
}

// Group is a GroupMe group, returned in JSON API responses
type Group struct {
	ID   ID     `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// Type of group (private|public)
	Type          string        `json:"type,omitempty"`
	Description   string        `json:"description,omitempty"`
	ImageURL      string        `json:"image_url,omitempty"`
	CreatorUserID ID            `json:"creator_user_id,omitempty"`
	CreatedAt     Timestamp     `json:"created_at,omitempty"`
	UpdatedAt     Timestamp     `json:"updated_at,omitempty"`
	Members       []*Member     `json:"members,omitempty"`
	ShareURL      string        `json:"share_url,omitempty"`
	Messages      GroupMessages `json:"messages,omitempty"`
}

// GroupMessages is a Group field, only returned in Group JSON API responses
type GroupMessages struct {
	Count                uint           `json:"count,omitempty"`
	LastMessageID        ID             `json:"last_message_id,omitempty"`
	LastMessageCreatedAt Timestamp      `json:"last_message_created_at,omitempty"`
	Preview              MessagePreview `json:"preview,omitempty"`
}

// MessagePreview is a GroupMessages field, only returned in Group JSON API responses.
// Abbreviated form of Message type
type MessagePreview struct {
	Nickname    string        `json:"nickname,omitempty"`
	Text        string        `json:"text,omitempty"`
	ImageURL    string        `json:"image_url,omitempty"`
	Attachments []*Attachment `json:"attachments,omitempty"`
}

// GetMemberByUserID gets the group member by their UserID,
// nil if no member matches
func (g *Group) GetMemberByUserID(userID ID) *Member {
	for _, member := range g.Members {
		if member.UserID == userID {
			return member
		}
	}

	return nil
}

// GetMemberByNickname gets the group member by their Nickname,
// nil if no member matches
func (g *Group) GetMemberByNickname(nickname string) *Member {
	for _, member := range g.Members {
		if member.Nickname == nickname {
			return member
		}
	}

	return nil
}

func (g *Group) String() string {
	return marshal(g)
}

// Member is a GroupMe group member, returned in JSON API responses
type Member struct {
	ID           ID     `json:"id,omitempty"`
	UserID       ID     `json:"user_id,omitempty"`
	Nickname     string `json:"nickname,omitempty"`
	Name         string `json:"name,omitempty"` // Account-wide name; Nickname is per-group.
	Muted        bool   `json:"muted,omitempty"`
	ImageURL     string `json:"image_url,omitempty"`
	AutoKicked   bool   `json:"autokicked,omitempty"`
	AppInstalled bool   `json:"app_installed,omitempty"`
	GUID         string `json:"guid,omitempty"`
	PhoneNumber  string `json:"phone_number,omitempty"` // Only used when searching for the member to add to a group.
	Email        string `json:"email,omitempty"`        // Only used when searching for the member to add to a group.
}

func (m *Member) String() string {
	return marshal(m)
}

// Message is a GroupMe group message, returned in JSON API responses
type Message struct {
	ID          ID         `json:"id,omitempty"`
	SourceGUID  string     `json:"source_guid,omitempty"`
	CreatedAt   Timestamp  `json:"created_at,omitempty"`
	GroupID     ID         `json:"group_id,omitempty"`
	UserID      ID         `json:"user_id,omitempty"`
	BotID       ID         `json:"bot_id,omitempty"`
	SenderID    ID         `json:"sender_id,omitempty"`
	SenderType  senderType `json:"sender_type,omitempty"`
	System      bool       `json:"system,omitempty"`
	Name        string     `json:"name,omitempty"`
	RecipientID ID         `json:"recipient_id,omitempty"`
	//ChatID - over push ConversationID seems to be called ChatID
	ChatID         ID     `json:"chat_id,omitempty"`
	ConversationID ID     `json:"conversation_id,omitempty"`
	AvatarURL      string `json:"avatar_url,omitempty"`
	// Maximum length of 1000 characters
	Text string `json:"text,omitempty"`
	// Must be an image service URL (i.groupme.com)
	ImageURL    string        `json:"image_url,omitempty"`
	FavoritedBy []string      `json:"favorited_by,omitempty"`
	Attachments []*Attachment `json:"attachments,omitempty"`
	// Reactions carries GroupMe's newer per-emoji reaction data (each
	// entry is one emoji, with the list of users who reacted with it).
	// This is distinct from -- and newer than -- FavoritedBy, which only
	// ever represented a single undifferentiated "like" (this library was
	// pinned before GroupMe added per-emoji reactions; not present
	// upstream, added locally). Confirmed live against a real message: a
	// 👍 reaction shows up here with code "\U0001F44D", NOT in FavoritedBy
	// alone in any way that identifies which emoji was used.
	Reactions []Reaction `json:"reactions,omitempty"`
	// Event carries structured data for GroupMe's "system event" messages
	// -- currently only used here for polls (poll.created/poll.reminder/
	// poll.finished), added well after this library was last touched, so
	// not present upstream. See PollEventData and Event's own doc comment.
	Event *Event `json:"event,omitempty"`
}

// Reaction is one emoji's worth of reactions on a message: the emoji
// itself (as a literal unicode string, e.g. "❤️" or
// "\U0001F44D") and every user who reacted with it. See Message.Reactions.
type Reaction struct {
	Type    string   `json:"type,omitempty"`
	Code    string   `json:"code,omitempty"`
	UserIDs []string `json:"user_ids,omitempty"`
}

// Event is GroupMe's envelope for "system event" messages -- structured
// data attached to an otherwise-normal Message. Not documented on
// dev.groupme.com (added after this library was last touched); confirmed
// against real live API responses. Data's shape depends on Type; see
// PollEventData for the only Type family currently parsed here
// ("poll.created"/"poll.reminder"/"poll.finished").
type Event struct {
	Type string          `json:"type,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// PollEventEntity is the small {id, nickname}-shaped user reference
// embedded in a poll.created event's Data.User.
type PollEventEntity struct {
	ID       string `json:"id,omitempty"`
	Nickname string `json:"nickname,omitempty"`
}

// PollEventPoll is the poll summary embedded in every poll.* event's
// Data.Poll. Only Expiration is populated on poll.reminder; only ID and
// Subject on poll.created/poll.finished -- callers needing the full
// definition (options, status, visibility, etc) for an in-progress poll
// should call Client.GetPoll (poll_api.go) with this ID instead.
type PollEventPoll struct {
	ID         string    `json:"id,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	Expiration Timestamp `json:"expiration,omitempty"`
}

// PollEventOption is one option's final tally, as embedded in a
// poll.finished event's Data.Options. VoterIDs is only present for a
// non-anonymous poll (unconfirmed live -- every poll observed so far was
// anonymous and only carried Votes).
type PollEventOption struct {
	ID       string   `json:"id,omitempty"`
	Title    string   `json:"title,omitempty"`
	Votes    int      `json:"votes,omitempty"`
	VoterIDs []string `json:"voter_ids,omitempty"`
}

// PollEventData is the parsed shape of a poll.*-type Event's Data field.
// Which fields are populated depends on Type: poll.created has Poll+User,
// poll.reminder has just Poll (with Expiration set), poll.finished has
// Poll+Options (final results). Conversation.ID is present on all three
// and is the same ID GetPoll expects as conversationID.
type PollEventData struct {
	Conversation struct {
		ID string `json:"id,omitempty"`
	} `json:"conversation,omitempty"`
	Poll    PollEventPoll     `json:"poll,omitempty"`
	User    PollEventEntity   `json:"user,omitempty"`
	Options []PollEventOption `json:"options,omitempty"`
}

func (m *Message) String() string {
	return marshal(m)
}

type senderType string

// SenderType constants
const (
	SenderTypeUser   senderType = "user"
	SenderTypeBot    senderType = "bot"
	SenderTypeSystem senderType = "system"
)

type attachmentType string

// AttachmentType constants
const (
	Mentions attachmentType = "mentions"
	Image    attachmentType = "image"
	Location attachmentType = "location"
	Emoji    attachmentType = "emoji"
	// Video, File, and Reply: local additions, not in the pinned upstream
	// version of this file (which predates this bridge having ever
	// handled them). Confirmed against the pre-2023 bridge's own
	// attachment-type switch as the real wire values GroupMe uses.
	Video attachmentType = "video"
	File  attachmentType = "file"
	Reply attachmentType = "reply"
	// Poll: a message announcing a new poll carries one of these
	// (Attachment.PollID) alongside a "poll.created" Event (see json.go's
	// Event/PollEventData) with the actual details -- this attachment on
	// its own is just a pointer, not enough to render the poll.
	Poll attachmentType = "poll"
)

// Attachment is a GroupMe message attachment, returned in JSON API responses
type Attachment struct {
	Type            attachmentType `json:"type,omitempty"`
	Loci            [][]int        `json:"loci,omitempty"`
	UserIDs         []ID           `json:"user_ids,omitempty"`
	URL             string         `json:"url,omitempty"`
	FileID          string         `json:"file_id,omitempty"`
	VideoPreviewURL string         `json:"preview_url,omitempty"`
	Name            string         `json:"name,omitempty"`
	Latitude        string         `json:"lat,omitempty"`
	Longitude       string         `json:"lng,omitempty"`
	Placeholder     string         `json:"placeholder,omitempty"`
	Charmap         [][]int        `json:"charmap,omitempty"`
	ReplyID         ID             `json:"reply_id,omitempty"`
	BaseReplyID     ID             `json:"base_reply_id,omitempty"`
	UserID          ID             `json:"user_id,omitempty"`
	// PollID: local addition, present on a "poll" attachment. See the
	// Poll attachmentType constant's doc comment.
	PollID string `json:"poll_id,omitempty"`
}

func (a *Attachment) String() string {
	return marshal(a)
}

// User is a GroupMe user, returned in JSON API responses
type User struct {
	ID          ID          `json:"id,omitempty"`
	PhoneNumber PhoneNumber `json:"phone_number,omitempty"`
	ImageURL    string      `json:"image_url,omitempty"`
	Name        string      `json:"name,omitempty"`
	CreatedAt   Timestamp   `json:"created_at,omitempty"`
	UpdatedAt   Timestamp   `json:"updated_at,omitempty"`
	AvatarURL   string      `json:"avatar_url,omitempty"`
	Email       string      `json:"email,omitempty"`
	SMS         bool        `json:"sms,omitempty"`
}

func (u *User) String() string {
	return marshal(u)
}

// Chat is a GroupMe direct message conversation between two users,
// returned in JSON API responses
type Chat struct {
	CreatedAt     Timestamp `json:"created_at,omitempty"`
	UpdatedAt     Timestamp `json:"updated_at,omitempty"`
	LastMessage   *Message  `json:"last_message,omitempty"`
	MessagesCount int       `json:"messages_count,omitempty"`
	OtherUser     User      `json:"other_user,omitempty"`
}

func (c *Chat) String() string {
	return marshal(c)
}

// Bot is a GroupMe bot, it is connected to a specific group which it can send messages to
type Bot struct {
	BotID          ID     `json:"bot_id,omitempty"`
	GroupID        ID     `json:"group_id,omitempty"`
	Name           string `json:"name,omitempty"`
	AvatarURL      string `json:"avatar_url,omitempty"`
	CallbackURL    string `json:"callback_url,omitempty"`
	DMNotification bool   `json:"dm_notification,omitempty"`
}

func (b *Bot) String() string {
	return marshal(b)
}

// Block is a GroupMe block between two users, direct messages are not allowed
type Block struct {
	UserID        ID        `json:"user_id,omitempty"`
	BlockedUserID ID        `json:"blocked_user_id,omitempty"`
	CreatedAT     Timestamp `json:"created_at,omitempty"`
}

func (b Block) String() string {
	return marshal(&b)
}

// Superficially increases test coverage
func marshal(i interface{}) string {
	bytes, err := json.MarshalIndent(i, "", "\t")
	if err != nil {
		return ""
	}

	return string(bytes)
}
