package groupmeExt

import (
	"context"

	"github.com/karmanyaahm/groupme"
	"github.com/karmanyaahm/matrix-groupme-go/types"
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
func (c Client) IndexAllGroups() ([]*groupme.Group, error) {
	return c.IndexGroups(context.TODO(), &groupme.GroupsQuery{
		//	Omit:    "memberships",
		PerPage: 100, //TODO: Configurable and add multipage support
	})
}

func (c Client) LoadMessagesAfter(groupID, lastMessageID string, lastMessageFromMe bool, num int) ([]*groupme.Message, error) {
	//TODO: limit max 100
	i, e := c.IndexMessages(context.TODO(), groupme.ID(groupID), &groupme.IndexMessagesQuery{
		AfterID: groupme.ID(lastMessageID),
		Limit:   num,
	})

	if e != nil {
		return nil, e
	}
	return i.Messages, nil
}

func (c Client) LoadMessagesBefore(groupID, lastMessageID string, num int) ([]*groupme.Message, error) {
	//TODO: limit max 100
	i, e := c.IndexMessages(context.TODO(), groupme.ID(groupID), &groupme.IndexMessagesQuery{
		BeforeID: groupme.ID(lastMessageID),
		Limit:    num,
	})
	//fmt.Println(groupID, lastMessageID, num, i.Count, e)
	if e != nil {
		return nil, e
	}
	return i.Messages, nil
}

func (c *Client) RemoveFromGroup(uid, groupID types.GroupMeID) error {

	group, err := c.ShowGroup(context.TODO(), groupme.ID(groupID))
	if err != nil {
		return err
	}
	return c.RemoveMember(context.TODO(), groupme.ID(groupID), group.GetMemberByUserID(groupme.ID(uid)).ID)
}
