package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/beeper/groupme/pkg/groupmeext"
)

var _ bridgev2.PushableNetworkAPI = (*GMClient)(nil)

var pushCfg = &bridgev2.PushConfig{
	// gcm_defaultSenderId from GroupMe Android 16.25.9.
	FCM: &bridgev2.FCMPushConfig{SenderID: "836136099376"},
}

func (gc *GMClient) GetPushConfigs() *bridgev2.PushConfig {
	return pushCfg
}

func (gc *GMClient) RegisterPushNotifications(ctx context.Context, pushType bridgev2.PushType, token string) error {
	if pushType != bridgev2.PushTypeFCM {
		return fmt.Errorf("unsupported push type: %s", pushType)
	}
	if !gc.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	return groupmeext.RegisterFCMPush(ctx, gc.Meta.Token, token)
}
