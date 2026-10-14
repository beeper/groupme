package groupmeext

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/beeper/groupme-lib"
)

// MessageContent is the complete editable content in the native v4 API.
// Attachments must be sent even when empty; omitting them is not equivalent.
type MessageContent struct {
	Text        string                `json:"text"`
	Attachments []*groupme.Attachment `json:"attachments"`
}

type MessageUpdate struct {
	RevisionID groupme.ID        `json:"-"`
	Message    MessageContent    `json:"message"`
	MessageID  groupme.ID        `json:"message_id"`
	SenderID   groupme.ID        `json:"sender_id"`
	UpdatedAt  groupme.Timestamp `json:"updated_at"`
}

type EditedMessage struct {
	groupme.Message
	UpdatedAt groupme.Timestamp `json:"updated_at"`
}

type ReadCheckpoint struct {
	ConversationID    groupme.ID `json:"conversation_id"`
	LastReadMessageID groupme.ID `json:"last_read_message_id"`
}

var activityHTTPClient = &http.Client{Timeout: 30 * time.Second}

func activityRequest(ctx context.Context, token, method, path string, body, result any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.groupme.com"+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid GroupMe activity request")
	}
	req.Header.Set("X-Access-Token", token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := activityHTTPClient.Do(req)
	if err != nil {
		return mediaError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GroupMe activity request returned HTTP %d", resp.StatusCode)
	}
	if result != nil {
		if err = json.NewDecoder(io.LimitReader(resp.Body, 8*1024*1024)).Decode(result); err != nil {
			return fmt.Errorf("invalid GroupMe activity response: %w", err)
		}
	}
	return nil
}

// EditMessage follows the web client's group and DM PUT contracts (2026-10-13).
// For DMs chatID is the other user, not the canonical conversation ID.
func EditMessage(ctx context.Context, token string, isDM bool, chatID, messageID groupme.ID, content MessageContent) (*EditedMessage, error) {
	kind := "groups"
	if isDM {
		kind = "direct_messages"
	}
	if content.Attachments == nil {
		content.Attachments = []*groupme.Attachment{}
	}
	var resp struct {
		Response struct {
			Message EditedMessage `json:"message"`
		} `json:"response"`
	}
	err := activityRequest(ctx, token, http.MethodPut, "/v4/"+kind+"/"+url.PathEscape(string(chatID))+"/messages/"+url.PathEscape(string(messageID)), content, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Response.Message.ID != messageID {
		return nil, fmt.Errorf("GroupMe edit response has the wrong message ID")
	}
	return &resp.Response.Message, nil
}

func GetReadCheckpoints(ctx context.Context, token string) ([]ReadCheckpoint, error) {
	var resp struct {
		Response struct {
			Receipts []ReadCheckpoint `json:"receipts"`
		} `json:"response"`
	}
	err := activityRequest(ctx, token, http.MethodGet, "/v4/read_receipts", nil, &resp)
	return resp.Response.Receipts, err
}

// MarkRead updates the account's unread checkpoint. The v4 route encodes '+',
// whereas the peer-visible v3 DM receipt below requires a literal '+'.
func MarkRead(ctx context.Context, token string, conversationID, messageID groupme.ID) error {
	path := strings.ReplaceAll(url.PathEscape(string(conversationID)), "+", "%2B")
	return activityRequest(ctx, token, http.MethodPost, "/v4/read_receipts/"+path, struct {
		ID groupme.ID `json:"last_read_message_id"`
	}{messageID}, nil)
}

func SendDMReadReceipt(ctx context.Context, token string, conversationID groupme.ID) error {
	return activityRequest(ctx, token, http.MethodPost, "/v3/conversations/"+url.PathEscape(string(conversationID))+"/read_receipt", nil, nil)
}

// DeleteDM removes this account's conversation using Web's Delete chat action.
// It is separate from clearing history, blocking the peer, or leaving a group.
func DeleteDM(ctx context.Context, token string, conversationID groupme.ID) error {
	return activityRequest(ctx, token, http.MethodDelete, "/v3/chats/"+url.PathEscape(string(conversationID)), nil, nil)
}
