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
func (c Client) IndexAllGroups() ([]*groupme.Group, error) {
	return c.IndexGroups(context.TODO(), &groupme.GroupsQuery{
		//	Omit:    "memberships",
		PerPage: 100, //TODO: Configurable and add multipage support
	})
}

func (c Client) IndexAllRelations() ([]*groupme.User, error) {
	return c.IndexRelations(context.TODO())
}

func (c Client) IndexAllChats() ([]*groupme.Chat, error) {
	return c.IndexChats(context.TODO(), &groupme.IndexChatsQuery{
		PerPage: 100, //TODO?
	})
}

func (c Client) LoadMessagesAfter(groupID groupme.ID, lastMessageID string, lastMessageFromMe bool, private bool) ([]*groupme.Message, error) {
	if private {
		ans, e := c.IndexDirectMessages(context.TODO(), groupID.String(), &groupme.IndexDirectMessagesQuery{
			SinceID: groupme.ID(lastMessageID),
			//Limit:    num,
		})
		//fmt.Println(groupID, lastMessageID, num, i.Count, e)
		if e != nil {
			return nil, e
		}

		for i, j := 0, len(ans.Messages)-1; i < j; i, j = i+1, j-1 {
			ans.Messages[i], ans.Messages[j] = ans.Messages[j], ans.Messages[i]
		}
		return ans.Messages, nil
	} else {
		i, e := c.IndexMessages(context.TODO(), groupID, &groupme.IndexMessagesQuery{
			AfterID: groupme.ID(lastMessageID),
			//20 for consistency with dms
			Limit: 20,
		})
		//fmt.Println(groupID, lastMessageID, num, i.Count, e)
		if e != nil {
			return nil, e
		}
		return i.Messages, nil
	}
}

func (c Client) LoadMessagesBefore(groupID, lastMessageID string, private bool) ([]*groupme.Message, error) {
	if private {
		i, e := c.IndexDirectMessages(context.TODO(), groupID, &groupme.IndexDirectMessagesQuery{
			BeforeID: groupme.ID(lastMessageID),
			//Limit:    num,
		})
		//fmt.Println(groupID, lastMessageID, num, i.Count, e)
		if e != nil {
			return nil, e
		}
		return i.Messages, nil
	} else {
		//TODO: limit max 100
		i, e := c.IndexMessages(context.TODO(), groupme.ID(groupID), &groupme.IndexMessagesQuery{
			BeforeID: groupme.ID(lastMessageID),
			//20 for consistency with dms
			Limit: 20,
		})
		//fmt.Println(groupID, lastMessageID, num, i.Count, e)
		if e != nil {
			return nil, e
		}
		return i.Messages, nil
	}
}

func (c *Client) RemoveFromGroup(uid, groupID groupme.ID) error {
	group, err := c.ShowGroup(context.TODO(), groupID)
	if err != nil {
		return err
	}
	return c.RemoveMember(context.TODO(), groupID, group.GetMemberByUserID(uid).ID)
}
