package groupme

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	PushServer       = "https://push.groupme.com/faye"
	userChannel      = "/user/"
	groupChannel     = "/group/"
	dmChannel        = "/direct_message/"
	subscribeChannel = "/meta/subscribe"
)

var (
	ErrHandlerNotFound    = errors.New("Handler not found")
	ErrListenerNotStarted = errors.New("GroupMe listener not started")
)

type HandlerAll interface {
	Handler

	//of self
	HandlerText
	HandlerLike
	HandlerMembership

	//of group
	HandleGroupTopic
	HandleGroupAvatar
	HandleGroupName
	HandleGroupLikeIcon

	//of group members
	HandleMemberNewNickname
	HandleMemberNewAvatar
	HandleMembers
}
type Handler interface {
	HandleError(error)
}
type HandlerText interface {
	HandleTextMessage(Message)
}
type HandlerLike interface {
	HandleLike(Message)
}
type HandlerMembership interface {
	HandleJoin(ID)
}

// Group Handlers
type HandleGroupTopic interface {
	HandleGroupTopic(group ID, newTopic string)
}

type HandleGroupName interface {
	HandleGroupName(group ID, newName string)
}
type HandleGroupAvatar interface {
	HandleGroupAvatar(group ID, newAvatar string)
}
type HandleGroupLikeIcon interface {
	HandleLikeIcon(group ID, PackID, PackIndex int, Type string)
}

// Group member handlers
type HandleMemberNewNickname interface {
	HandleNewNickname(group ID, user ID, newName string)
}

type HandleMemberNewAvatar interface {
	HandleNewAvatarInGroup(group ID, user ID, avatarURL string)
}
type HandleMembers interface {
	//HandleNewMembers returns only partial member with id and nickname; added is false if removing
	HandleMembers(group ID, members []Member, added bool)
}

type PushMessage interface {
	Channel() string
	Data() map[string]interface{}
	Ext() map[string]interface{}
	Error() string
}

type FayeClient interface {
	Listen(ctx context.Context)
	WaitSubscribe(ctx context.Context, channel, token string, msgChannel chan PushMessage) error
}

type PushSubscription struct {
	channel       chan PushMessage
	fayeClient    FayeClient
	handlers      []Handler
	lastConnected atomic.Int64
	workers       sync.WaitGroup
}

// NewPushSubscription creates and returns a push subscription object
func NewPushSubscription(context context.Context) *PushSubscription {

	r := PushSubscription{
		channel: make(chan PushMessage, 256),
	}

	return &r
}

func (r *PushSubscription) AddHandler(h Handler) {
	r.handlers = append(r.handlers, h)
}

// AddFullHandler is the same as AddHandler except it ensures the interface implements everything
func (r *PushSubscription) AddFullHandler(h HandlerAll) {
	r.handlers = append(r.handlers, h)
}

var RealTimeHandlers map[string]func(r *PushSubscription, channel string, data ...interface{})
var RealTimeSystemHandlers map[string]func(r *PushSubscription, channel string, id ID, rawData []byte)

func (r *PushSubscription) StartListening(ctx context.Context, client FayeClient) {
	r.fayeClient = client
	r.workers.Add(2)
	go func() {
		defer r.workers.Done()
		client.Listen(ctx)
	}()
	go func() {
		defer r.workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-r.channel:
				if ctx.Err() != nil {
					return
				}
				r.lastConnected.Store(time.Now().Unix())
				data := msg.Data()
				contentType, _ := data["type"].(string)
				if handler := RealTimeHandlers[contentType]; handler != nil && data["subject"] != nil {
					handler(r, msg.Channel(), data["subject"])
				} else if contentType != "" && contentType != "ping" {
					log.Println("Unable to handle GroupMe message type", contentType)
				}
			}
		}
	}()
}

func (r *PushSubscription) Wait() { r.workers.Wait() }

// SubscribeToUser to users
func (r *PushSubscription) SubscribeToUser(context context.Context, id ID, authToken string) error {
	return r.subscribeWithPrefix(userChannel, context, id, authToken)
}

// SubscribeToGroup to groups for typing notification
func (r *PushSubscription) SubscribeToGroup(context context.Context, id ID, authToken string) error {
	return r.subscribeWithPrefix(groupChannel, context, id, authToken)
}

// SubscribeToDM to users
func (r *PushSubscription) SubscribeToDM(context context.Context, id ID, authToken string) error {
	id = ID(strings.Replace(id.String(), "+", "_", 1))
	return r.subscribeWithPrefix(dmChannel, context, id, authToken)
}

func (r *PushSubscription) subscribeWithPrefix(prefix string, ctx context.Context, groupID ID, authToken string) error {
	if r.fayeClient == nil {
		return ErrListenerNotStarted
	}
	return r.fayeClient.WaitSubscribe(ctx, prefix+groupID.String(), authToken, r.channel)
}

func (r *PushSubscription) Connected() bool {
	return r.lastConnected.Load()+30 >= time.Now().Unix()
}
