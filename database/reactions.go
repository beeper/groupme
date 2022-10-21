package database

import (
	"database/sql"
	"errors"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"github.com/beeper/groupme-lib"
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

const (
	getReactionByTargetGMIDQuery = `
		SELECT chat_gmid, chat_receiver, target_gmid, sender, mxid, gmid
		FROM reaction
		WHERE chat_gmid=$1 AND chat_receiver=$2 AND target_gmid=$3 AND sender=$4
	`
	getReactionByMXIDQuery = `
		SELECT chat_gmid, chat_receiver, target_gmid, sender, mxid, gmid FROM reaction
		WHERE mxid=$1
	`
	upsertReactionQuery = `
		INSERT INTO reaction (chat_gmid, chat_receiver, target_gmid, sender, mxid, gmid)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (chat_gmid, chat_receiver, target_gmid, sender)
			DO UPDATE SET mxid=excluded.mxid, gmid=excluded.gmid
	`
	deleteReactionQuery = `
		DELETE FROM reaction WHERE chat_gmid=$1 AND chat_receiver=$2 AND target_gmid=$3 AND sender=$4 AND mxid=$5
	`
)

func (rq *ReactionQuery) GetByTargetGMID(chat PortalKey, gmid groupme.ID, sender groupme.ID) *Reaction {
	return rq.maybeScan(rq.db.QueryRow(getReactionByTargetGMIDQuery, chat.GMID, chat.Receiver, gmid, sender))
}

func (rq *ReactionQuery) GetByMXID(mxid id.EventID) *Reaction {
	return rq.maybeScan(rq.db.QueryRow(getReactionByMXIDQuery, mxid))
}

func (rq *ReactionQuery) maybeScan(row *sql.Row) *Reaction {
	if row == nil {
		return nil
	}
	return rq.New().Scan(row)
}

type Reaction struct {
	db  *Database
	log log.Logger

	Chat       PortalKey
	TargetGMID groupme.ID
	Sender     groupme.ID
	MXID       id.EventID
	GMID       groupme.ID
}

func (reaction *Reaction) Scan(row dbutil.Scannable) *Reaction {
	err := row.Scan(&reaction.Chat.GMID, &reaction.Chat.Receiver, &reaction.TargetGMID, &reaction.Sender, &reaction.MXID, &reaction.GMID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			reaction.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	return reaction
}

func (reaction *Reaction) Upsert(txn dbutil.Execable) {
	if txn == nil {
		txn = reaction.db
	}
	_, err := txn.Exec(upsertReactionQuery, reaction.Chat.GMID, reaction.Chat.Receiver, reaction.TargetGMID, reaction.Sender, reaction.MXID, reaction.GMID)
	if err != nil {
		reaction.log.Warnfln("Failed to upsert reaction to %s@%s by %s: %v", reaction.Chat, reaction.TargetGMID, reaction.Sender, err)
	}
}

func (reaction *Reaction) GetTarget() *Message {
	return reaction.db.Message.GetByGMID(reaction.Chat, reaction.TargetGMID)
}

func (reaction *Reaction) Delete() {
	_, err := reaction.db.Exec(deleteReactionQuery, reaction.Chat.GMID, reaction.Chat.Receiver, reaction.TargetGMID, reaction.Sender, reaction.MXID)
	if err != nil {
		reaction.log.Warnfln("Failed to delete reaction %s: %v", reaction.MXID, err)
	}
}
