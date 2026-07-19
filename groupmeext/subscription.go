package groupmeext

import (
	log "maunium.net/go/maulogger/v2"

	"github.com/beeper/groupme-lib"
)

type fayeLogger struct {
	log.Logger
}

func (f fayeLogger) Debugf(i string, a ...interface{}) {
	f.Logger.Debugfln(i, a...)
}
func (f fayeLogger) Errorf(i string, a ...interface{}) {
	f.Logger.Errorfln(i, a...)
}
func (f fayeLogger) Warnf(i string, a ...interface{}) {
	f.Logger.Warnfln(i, a...)
}
func (f fayeLogger) Infof(i string, a ...interface{}) {
	f.Logger.Infofln(i, a...)
}

// NewFayeClient creates a WebSocket-based Faye client for GroupMe's push
// service. GroupMe stopped answering Bayeux HTTP long-polling handshakes,
// so the connection must be made over WebSocket.
func NewFayeClient(logger log.Logger) *groupme.WSFayeClient {
	return groupme.NewWSFayeClient(groupme.PushServer, fayeLogger{logger.Sub("FayeClient")})
}
