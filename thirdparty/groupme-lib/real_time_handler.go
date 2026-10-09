package groupme

import (
	"encoding/json"
	"strconv"
)

// Modern push events describe the reacting user separately from the message
// author. In particular, like.delete doesn't include a full reaction snapshot.
func reactionHandler(remove bool) func(*PushSubscription, string, ...interface{}) {
	return func(r *PushSubscription, _ string, data ...interface{}) {
		if len(data) == 0 {
			return
		}
		raw, err := json.Marshal(data[0])
		if err != nil {
			return
		}
		var subject struct {
			Line          Message   `json:"line"`
			DirectMessage Message   `json:"direct_message"`
			UserID        ID        `json:"user_id"`
			UserReaction  *Reaction `json:"user_reaction"`
		}
		if json.Unmarshal(raw, &subject) != nil || subject.UserID == "" {
			return
		}
		msg := subject.Line
		if msg.ID == "" {
			msg = subject.DirectMessage
		}
		if msg.ID == "" {
			return
		}
		if remove {
			subject.UserReaction = nil
		} else if subject.UserReaction == nil || subject.UserReaction.Type != "unicode" || subject.UserReaction.Code == "" {
			return
		}
		for _, h := range r.handlers {
			if h, ok := h.(HandlerReaction); ok {
				h.HandleReaction(msg, subject.UserID, subject.UserReaction)
			}
		}
	}
}

func init() {

	RealTimeHandlers = make(map[string]func(r *PushSubscription, channel string, data ...interface{}))

	//Base Handlers on user channel
	RealTimeHandlers["direct_message.create"] = func(r *PushSubscription, channel string, data ...interface{}) {
		if len(data) == 0 {
			return
		}
		b, err := json.Marshal(data[0])
		if err != nil {
			return
		}
		var out Message
		if json.Unmarshal(b, &out) != nil || out.ID == "" {
			return
		}
		if out.ConversationID == "" {
			out.ConversationID = out.ChatID
		}
		if out.UserID == "system" && out.Event != nil {
			if handler := RealTimeSystemHandlers[out.Event.Type]; handler != nil {
				id := out.GroupID
				if id == "" {
					id = out.ConversationID
				}
				handler(r, channel, id, out.Event.Data)
				return
			}
			// Polls and other user-visible system messages still have useful
			// content. Let the network connector convert the native envelope.
		}

		for _, h := range r.handlers {
			if h, ok := h.(HandlerText); ok {
				h.HandleTextMessage(out)
			}
		}
	}

	RealTimeHandlers["line.create"] = RealTimeHandlers["direct_message.create"]

	RealTimeHandlers["like.create"] = reactionHandler(false)
	RealTimeHandlers["like.delete"] = reactionHandler(true)

	RealTimeHandlers["membership.create"] = func(r *PushSubscription, channel string, data ...interface{}) {
		c, _ := data[0].(map[string]interface{})
		id, _ := c["id"].(string)

		for _, h := range r.handlers {
			if h, ok := h.(HandlerMembership); ok {
				h.HandleJoin(ID(id))
			}
		}

	}

	// Chat-channel favorite events contain a complete reaction snapshot next
	// to the target message, not inside it. Older payloads used message fields.
	RealTimeHandlers["favorite"] = func(r *PushSubscription, channel string, data ...interface{}) {
		if len(data) == 0 {
			return
		}
		raw, err := json.Marshal(data[0])
		if err != nil {
			return
		}
		var subject struct {
			Line          Message    `json:"line"`
			DirectMessage Message    `json:"direct_message"`
			Reactions     []Reaction `json:"reactions"`
		}
		if json.Unmarshal(raw, &subject) != nil {
			return
		}
		msg := subject.Line
		if msg.ID == "" {
			msg = subject.DirectMessage
		}
		if msg.ID == "" {
			return
		}
		if subject.Reactions != nil {
			msg.Reactions = subject.Reactions
		} else if msg.Reactions == nil && msg.FavoritedBy == nil {
			// Missing state is not an empty snapshot: do not remove reactions.
			return
		}
		for _, h := range r.handlers {
			if h, ok := h.(HandlerLike); ok {
				h.HandleLike(msg)
			}
		}
	}

	//following are for messages from system (administrative/settings changes)
	RealTimeSystemHandlers = make(map[string]func(r *PushSubscription, channel string, id ID, rawData []byte))

	RealTimeSystemHandlers["membership.nickname_changed"] = func(r *PushSubscription, channel string, id ID, rawData []byte) {
		thing := struct {
			Name string
			User struct {
				ID int
			}
		}{}
		_ = json.Unmarshal(rawData, &thing)

		for _, h := range r.handlers {
			if h, ok := h.(HandleMemberNewNickname); ok {
				h.HandleNewNickname(id, ID(strconv.Itoa(thing.User.ID)), thing.Name)
			}
		}

	}

	RealTimeSystemHandlers["membership.avatar_changed"] = func(r *PushSubscription, channel string, id ID, rawData []byte) {
		content := struct {
			AvatarURL string `json:"avatar_url"`
			User      struct {
				ID int
			}
		}{}
		_ = json.Unmarshal(rawData, &content)

		for _, h := range r.handlers {
			if h, ok := h.(HandleMemberNewAvatar); ok {
				h.HandleNewAvatarInGroup(id, ID(strconv.Itoa(content.User.ID)), content.AvatarURL)
			}
		}

	}

	RealTimeSystemHandlers["membership.announce.added"] = func(r *PushSubscription, channel string, id ID, rawData []byte) {
		data := struct {
			Added []Member `json:"added_users"`
		}{}
		_ = json.Unmarshal(rawData, &data)
		for _, h := range r.handlers {
			if h, ok := h.(HandleMembers); ok {
				h.HandleMembers(id, data.Added, true)
			}
		}
	}

	RealTimeSystemHandlers["membership.notifications.removed"] = func(r *PushSubscription, channel string, id ID, rawData []byte) {
		data := struct {
			Added Member `json:"removed_user"`
		}{}
		_ = json.Unmarshal(rawData, &data)
		for _, h := range r.handlers {
			if h, ok := h.(HandleMembers); ok {
				h.HandleMembers(id, []Member{data.Added}, false)
			}
		}

	}

	RealTimeSystemHandlers["membership.name_change"] = func(r *PushSubscription, channel string, id ID, rawData []byte) {

		data := struct {
			Name string
		}{}
		_ = json.Unmarshal(rawData, &data)

		for _, h := range r.handlers {
			if h, ok := h.(HandleGroupName); ok {
				h.HandleGroupName(id, data.Name)
			}
		}
	}

	RealTimeSystemHandlers["group.name_change"] = func(r *PushSubscription, channel string, id ID, rawData []byte) {

		data := struct {
			Name string
		}{}
		_ = json.Unmarshal(rawData, &data)

		for _, h := range r.handlers {
			if h, ok := h.(HandleGroupName); ok {
				h.HandleGroupName(id, data.Name)
			}
		}
	}

	RealTimeSystemHandlers["group.topic_change"] = func(r *PushSubscription, channel string, id ID, rawData []byte) {

		data := struct {
			Topic string
		}{}
		_ = json.Unmarshal(rawData, &data)

		for _, h := range r.handlers {
			if h, ok := h.(HandleGroupTopic); ok {
				h.HandleGroupTopic(id, data.Topic)
			}
		}
	}

	RealTimeSystemHandlers["group.avatar_change"] = func(r *PushSubscription, channel string, id ID, rawData []byte) {
		data := struct {
			AvatarURL string `json:"avatar_url"`
		}{}
		_ = json.Unmarshal(rawData, &data)

		for _, h := range r.handlers {
			if h, ok := h.(HandleGroupAvatar); ok {
				h.HandleGroupAvatar(id, data.AvatarURL)
			}
		}
	}

	RealTimeSystemHandlers["group.like_icon_set"] = func(r *PushSubscription, channel string, id ID, rawData []byte) {
		data := struct {
			LikeIcon struct {
				PackID    int `json:"pack_id"`
				PackIndex int `json:"pack_index"`
				Type      string
			} `json:"like_icon"`
		}{}
		_ = json.Unmarshal(rawData, &data)

		for _, h := range r.handlers {
			if h, ok := h.(HandleGroupLikeIcon); ok {
				h.HandleLikeIcon(id, data.LikeIcon.PackID, data.LikeIcon.PackIndex, data.LikeIcon.Type)
			}
		}
	}

	RealTimeSystemHandlers["group.like_icon_removed"] = func(r *PushSubscription, channel string, id ID, rawData []byte) {
		for _, h := range r.handlers {
			if h, ok := h.(HandleGroupLikeIcon); ok {
				h.HandleLikeIcon(id, 0, 0, "")
			}
		}
	}

}
