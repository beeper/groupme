package connector

import (
	"context"
	"fmt"

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/beeper/groupme-lib"
)

var (
	_ bridgev2.ContactListingNetworkAPI      = (*GMClient)(nil)
	_ bridgev2.IdentifierResolvingNetworkAPI = (*GMClient)(nil)
)

func (gc *GMClient) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	users, err := gc.Client.IndexRelations(ctx)
	if err != nil {
		return nil, err
	}
	contacts := make([]*bridgev2.ResolveIdentifierResponse, 0, len(users))
	for _, user := range users {
		if user == nil || user.ID == "" || string(user.ID) == gc.Meta.GMID {
			continue
		}
		contacts = append(contacts, &bridgev2.ResolveIdentifierResponse{
			UserID:   MakeUserID(user.ID),
			UserInfo: &bridgev2.UserInfo{Name: ptr.Ptr(user.Name), Avatar: gc.avatarIfSet(ctx, user.AvatarURL)},
		})
	}
	return contacts, nil
}

// GroupMe creates a direct conversation on the first send. Resolve only known
// contacts; don't advertise arbitrary username, phone, or email lookup.
func (gc *GMClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	contacts, err := gc.GetContactList(ctx)
	if err != nil {
		return nil, err
	}
	for _, contact := range contacts {
		if string(contact.UserID) != identifier {
			continue
		}
		if createChat {
			contact.Chat = &bridgev2.CreateChatResponse{PortalKey: gc.portalKeyForDM(groupme.ID(contact.UserID))}
		}
		return contact, nil
	}
	return nil, fmt.Errorf("no GroupMe contact found with that user ID")
}
