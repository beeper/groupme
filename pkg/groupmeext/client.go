package groupmeext

import (
	"context"

	"github.com/beeper/groupme-lib"
)

type Client struct {
	*groupme.Client
}

// NewClient creates a new GroupMe API Client
func NewClient(authToken string) *Client {
	n := Client{
		Client: groupme.NewClient(authToken),
	}
	return &n
}
func (c Client) IndexAllGroups(ctx context.Context) ([]*groupme.Group, error) {
	var groups []*groupme.Group
	for page := 1; ; page++ {
		batch, err := c.IndexGroups(ctx, &groupme.GroupsQuery{Page: page, PerPage: 100})
		if err != nil {
			return nil, err
		}
		groups = append(groups, batch...)
		if len(batch) < 100 {
			return groups, nil
		}
	}
}

func (c Client) IndexAllChats(ctx context.Context) ([]*groupme.Chat, error) {
	var chats []*groupme.Chat
	for page := 1; ; page++ {
		batch, err := c.IndexChats(ctx, &groupme.IndexChatsQuery{Page: page, PerPage: 100})
		if err != nil {
			return nil, err
		}
		chats = append(chats, batch...)
		if len(batch) < 100 {
			return chats, nil
		}
	}
}
