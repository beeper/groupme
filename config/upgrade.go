package config

import (
	"strings"

	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/util"
	up "maunium.net/go/mautrix/util/configupgrade"
)

func DoUpgrade(helper *up.Helper) {
	bridgeconfig.Upgrader.DoUpgrade(helper)

	helper.Copy(up.Str|up.Null, "segment_key")

	helper.Copy(up.Bool, "metrics", "enabled")
	helper.Copy(up.Str, "metrics", "listen")

	helper.Copy(up.Int, "groupme", "connection_timeout")
	helper.Copy(up.Bool, "groupme", "fetch_message_on_timeout")

	helper.Copy(up.Str, "bridge", "username_template")
	helper.Copy(up.Str, "bridge", "displayname_template")
	helper.Copy(up.Bool, "bridge", "personal_filtering_spaces")
	helper.Copy(up.Bool, "bridge", "delivery_receipts")
	helper.Copy(up.Bool, "bridge", "message_status_events")
	helper.Copy(up.Bool, "bridge", "message_error_notices")

	helper.Copy(up.Int, "bridge", "portal_message_buffer")
	helper.Copy(up.Bool, "bridge", "call_start_notices")
	helper.Copy(up.Bool, "bridge", "identity_change_notices")
	helper.Copy(up.Bool, "bridge", "user_avatar_sync")
	helper.Copy(up.Bool, "bridge", "bridge_matrix_leave")
	helper.Copy(up.Bool, "bridge", "sync_with_custom_puppets")
	helper.Copy(up.Bool, "bridge", "sync_direct_chat_list")
	helper.Copy(up.Bool, "bridge", "default_bridge_receipts")
	helper.Copy(up.Bool, "bridge", "default_bridge_presence")
	helper.Copy(up.Bool, "bridge", "send_presence_on_typing")
	helper.Copy(up.Bool, "bridge", "force_active_delivery_receipts")
	helper.Copy(up.Map, "bridge", "double_puppet_server_map")
	helper.Copy(up.Bool, "bridge", "double_puppet_allow_discovery")
	if legacySecret, ok := helper.Get(up.Str, "bridge", "login_shared_secret"); ok && len(legacySecret) > 0 {
		baseNode := helper.GetBaseNode("bridge", "login_shared_secret_map")
		baseNode.Map[helper.GetBase("homeserver", "domain")] = up.StringNode(legacySecret)
		baseNode.UpdateContent()
	} else {
		helper.Copy(up.Map, "bridge", "login_shared_secret_map")
	}
	helper.Copy(up.Bool, "bridge", "private_chat_portal_meta")
	helper.Copy(up.Bool, "bridge", "parallel_member_sync")
	helper.Copy(up.Bool, "bridge", "bridge_notices")
	helper.Copy(up.Bool, "bridge", "resend_bridge_info")
	helper.Copy(up.Bool, "bridge", "mute_bridging")
	helper.Copy(up.Str|up.Null, "bridge", "archive_tag")
	helper.Copy(up.Str|up.Null, "bridge", "pinned_tag")
	helper.Copy(up.Bool, "bridge", "tag_only_on_create")
	helper.Copy(up.Bool, "bridge", "enable_status_broadcast")
	helper.Copy(up.Bool, "bridge", "disable_status_broadcast_send")
	helper.Copy(up.Bool, "bridge", "mute_status_broadcast")
	helper.Copy(up.Str|up.Null, "bridge", "status_broadcast_tag")
	helper.Copy(up.Bool, "bridge", "whatsapp_thumbnail")
	helper.Copy(up.Bool, "bridge", "allow_user_invite")
	helper.Copy(up.Str, "bridge", "command_prefix")
	helper.Copy(up.Bool, "bridge", "federate_rooms")
	helper.Copy(up.Bool, "bridge", "disappearing_messages_in_groups")
	helper.Copy(up.Bool, "bridge", "disable_bridge_alerts")
	helper.Copy(up.Bool, "bridge", "crash_on_stream_replaced")
	helper.Copy(up.Bool, "bridge", "url_previews")
	helper.Copy(up.Bool, "bridge", "caption_in_message")
	helper.Copy(up.Bool, "bridge", "send_whatsapp_edits")
	helper.Copy(up.Str|up.Null, "bridge", "message_handling_timeout", "error_after")
	helper.Copy(up.Str|up.Null, "bridge", "message_handling_timeout", "deadline")

	helper.Copy(up.Str, "bridge", "management_room_text", "welcome")
	helper.Copy(up.Str, "bridge", "management_room_text", "welcome_connected")
	helper.Copy(up.Str, "bridge", "management_room_text", "welcome_unconnected")
	helper.Copy(up.Str|up.Null, "bridge", "management_room_text", "additional_help")
	helper.Copy(up.Bool, "bridge", "encryption", "allow")
	helper.Copy(up.Bool, "bridge", "encryption", "default")
	helper.Copy(up.Bool, "bridge", "encryption", "require")
	helper.Copy(up.Bool, "bridge", "encryption", "appservice")
	helper.Copy(up.Str, "bridge", "encryption", "verification_levels", "receive")
	helper.Copy(up.Str, "bridge", "encryption", "verification_levels", "send")
	helper.Copy(up.Str, "bridge", "encryption", "verification_levels", "share")

	legacyKeyShareAllow, ok := helper.Get(up.Bool, "bridge", "encryption", "key_sharing", "allow")
	if ok {
		helper.Set(up.Bool, legacyKeyShareAllow, "bridge", "encryption", "allow_key_sharing")
		legacyKeyShareRequireCS, legacyOK1 := helper.Get(up.Bool, "bridge", "encryption", "key_sharing", "require_cross_signing")
		legacyKeyShareRequireVerification, legacyOK2 := helper.Get(up.Bool, "bridge", "encryption", "key_sharing", "require_verification")
		if legacyOK1 && legacyOK2 && legacyKeyShareRequireVerification == "false" && legacyKeyShareRequireCS == "false" {
			helper.Set(up.Str, "unverified", "bridge", "encryption", "verification_levels", "share")
		}
	} else {
		helper.Copy(up.Bool, "bridge", "encryption", "allow_key_sharing")
	}

	helper.Copy(up.Bool, "bridge", "encryption", "rotation", "enable_custom")
	helper.Copy(up.Int, "bridge", "encryption", "rotation", "milliseconds")
	helper.Copy(up.Int, "bridge", "encryption", "rotation", "messages")
	if prefix, ok := helper.Get(up.Str, "appservice", "provisioning", "prefix"); ok {
		helper.Set(up.Str, strings.TrimSuffix(prefix, "/v1"), "bridge", "provisioning", "prefix")
	} else {
		helper.Copy(up.Str, "bridge", "provisioning", "prefix")
	}
	if secret, ok := helper.Get(up.Str, "appservice", "provisioning", "shared_secret"); ok && secret != "generate" {
		helper.Set(up.Str, secret, "bridge", "provisioning", "shared_secret")
	} else if secret, ok = helper.Get(up.Str, "bridge", "provisioning", "shared_secret"); !ok || secret == "generate" {
		sharedSecret := util.RandomString(64)
		helper.Set(up.Str, sharedSecret, "bridge", "provisioning", "shared_secret")
	} else {
		helper.Copy(up.Str, "bridge", "provisioning", "shared_secret")
	}
	helper.Copy(up.Map, "bridge", "permissions")
	helper.Copy(up.Bool, "bridge", "relay", "enabled")
	helper.Copy(up.Bool, "bridge", "relay", "admin_only")
	helper.Copy(up.Map, "bridge", "relay", "message_formats")
}

var SpacedBlocks = [][]string{
	{"homeserver", "software"},
	{"appservice"},
	{"appservice", "hostname"},
	{"appservice", "database"},
	{"appservice", "id"},
	{"appservice", "as_token"},
	{"segment_key"},
	{"metrics"},
	{"groupme"},
	{"bridge"},
	{"bridge", "command_prefix"},
	{"bridge", "management_room_text"},
	{"bridge", "encryption"},
	{"bridge", "provisioning"},
	{"bridge", "permissions"},
	{"bridge", "relay"},
	{"logging"},
}
