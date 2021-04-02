package database

import (
	"github.com/karmanyaahm/matrix-groupme-go/types"
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type ReactionQuery struct {
	db  *Database
	log log.Logger
}

func (mq *ReactionQuery) New() *Reaction {
	return &Reaction{
		db:  mq.db,
		log: mq.log,
	}
}

func (mq *ReactionQuery) GetByJID(jid types.GroupMeID) (reactions []*Reaction) {
	ans := mq.db.Model(&Reaction{}).
		Preload("Puppet"). // TODO: Do this in seperate function?
		Where("message_jid = ?", jid).
		Limit(1).Find(&reactions)

	if ans.Error != nil || ans.RowsAffected == 0 {
		return nil
	}

	for _, reaction := range reactions {
		reaction.db = mq.db
		reaction.log = mq.log
	}

	return
}

//	ans := mq.db.Model(&Reaction{}).
//		Joins("INNER JOIN users on users.mxid = reactions.user_mxid").
//		Where("reactions.message_jid = ? AND users.jid = ?", jid, uid).
//		Limit(1).Find(&reactions)

type Reaction struct {
	db  *Database
	log log.Logger

	MXID id.EventID `gorm:"primaryKey"`

	//Message
	MessageJID  types.GroupMeID `gorm:"notNull"`
	MessageMXID id.EventID      `gorm:"notNull"`

	Message Message `gorm:"foreignKey:MessageMXID,MessageJID;references:MXID,JID;"`

	//User
	PuppetJID types.GroupMeID `gorm:"notNull"`
	Puppet    Puppet          `gorm:"foreignKey:PuppetJID;references:jid;"`
}

func (reaction *Reaction) Insert() {
	ans := reaction.db.Create(&reaction)
	if ans.Error != nil {
		reaction.log.Warnfln("Failed to insert %s@%s: %v", reaction.MXID, reaction.MessageJID, ans.Error)
	}
}

func (reaction *Reaction) Delete() {
	ans := reaction.db.Delete(&reaction)
	if ans.Error != nil {
		reaction.log.Warnfln("Failed to insert %s@%s: %v", reaction.MXID, reaction.MessageJID, ans.Error)
	}
}
