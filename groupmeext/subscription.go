package groupmeext

import (
	log "maunium.net/go/maulogger/v2"

	"github.com/karmanyaahm/wray"

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

type FayeClient struct {
	*wray.FayeClient
}

func (fc FayeClient) WaitSubscribe(channel string, msgChannel chan groupme.PushMessage) {
	c_new := make(chan wray.Message)
	fc.FayeClient.WaitSubscribe(channel, c_new)
	//converting between types because channels don't support interfaces well
	go func() {
		for i := range c_new {
			msgChannel <- i
		}
	}()
}

//for authentication, specific implementation will vary based on faye library
type AuthExt struct{}

func (a *AuthExt) In(wray.Message) {}
func (a *AuthExt) Out(m wray.Message) {
	groupme.OutMsgProc(m)
}

func NewFayeClient(logger log.Logger) *FayeClient {

	fc := &FayeClient{wray.NewFayeClient(groupme.PushServer)}
	fc.SetLogger(fayeLogger{logger.Sub("FayeClient")})
	fc.AddExtension(&AuthExt{})
	//fc.AddExtension(fc.FayeClient)

	return fc
}
