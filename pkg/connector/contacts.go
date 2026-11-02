package connector

import (
	"context"
	"net/http"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
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
		zerolog.Ctx(ctx).Err(err).Msg("Failed to fetch GroupMe contacts")
		return nil, wrapGroupMeRespError(err, "failed to fetch your GroupMe contacts")
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
	return nil, bridgev2.RespError(mautrix.MNotFound.WithMessage("no GroupMe contact found with that user ID"))
}

func wrapGroupMeRespError(err error, message string) error {
	switch groupMeHTTPStatus(err) {
	case http.StatusUnauthorized, http.StatusForbidden:
		return bridgev2.RespError(mautrix.MForbidden.WithMessage("your GroupMe session expired, please log in again"))
	case http.StatusTooManyRequests:
		return bridgev2.RespError(mautrix.MLimitExceeded.WithMessage("GroupMe is rate limiting requests, try again later"))
	}
	return bridgev2.RespError(mautrix.MUnknown.WithMessage(message).WithStatus(http.StatusBadGateway))
}
