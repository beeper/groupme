package groupmeext

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// DM "message requests": a DM from someone GroupMe doesn't consider a
// contact arrives with requires_approval=true on the chat, and replies
// don't go through until the request is accepted. Neither endpoint below is
// in dev.groupme.com's docs or the community docs; both were found in
// web.groupme.com's own client bundle (2026-09-28), which accepts a request
// with a bare POST to chats/<conversation_id>/approve, and verified live:
// the approve route returns 200 {"meta":{"code":200}} (a made-up action on
// the same path 500s), and is a harmless no-op on an already-accepted chat.
//
// The conversation ID must be the "smaller+larger" form (see
// connector.DMConversationID) with a literal "+" -- URL-encoding it as %2B
// 404s, and the bare other-user ID 400s ("bad chat id").

const chatsEndpoint = "https://api.groupme.com/v3/chats/"

// ChatRequiresApproval reports whether the DM chat is a pending message
// request the logged-in user hasn't accepted yet.
func ChatRequiresApproval(ctx context.Context, token, conversationID string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chatsEndpoint+conversationID, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Access-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("fetching chat: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("fetching chat: HTTP %d: %.200s", resp.StatusCode, body)
	}
	var parsed struct {
		Response struct {
			RequiresApproval bool `json:"requires_approval"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, fmt.Errorf("decoding chat: %w", err)
	}
	return parsed.Response.RequiresApproval, nil
}

// ApproveChat accepts a pending DM message request.
func ApproveChat(ctx context.Context, token, conversationID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatsEndpoint+conversationID+"/approve", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Access-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("approving chat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("approving chat: HTTP %d: %.200s", resp.StatusCode, body)
	}
	return nil
}
