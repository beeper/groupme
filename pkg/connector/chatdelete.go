package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/beeper/groupme/pkg/groupmeext"
)

var _ bridgev2.DeleteChatHandlingNetworkAPI = (*GMClient)(nil)

func (gc *GMClient) HandleMatrixDeleteChat(ctx context.Context, msg *bridgev2.MatrixDeleteChat) error {
	if msg.Content.DeleteForEveryone {
		return fmt.Errorf("GroupMe does not support deleting a DM for everyone")
	}
	kind, _ := ParsePortalID(msg.Portal.ID)
	if kind != PortalTypeDM {
		return fmt.Errorf("GroupMe groups must be left or ended in GroupMe; they cannot be deleted for yourself")
	}
	conversation, err := gc.conversationID(msg.Portal.ID)
	if err != nil {
		return err
	}
	return groupmeext.DeleteDM(ctx, gc.Meta.Token, conversation)
}

func chatDeleteEvent(key networkid.PortalKey) *simplevent.ChatDelete {
	if key.ID == "" {
		return nil
	}
	return &simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventChatDelete, PortalKey: key, Sender: bridgev2.EventSender{IsFromMe: true}},
		OnlyForMe: true,
	}
}
