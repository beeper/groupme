// mautrix-groupme - A Matrix-GroupMe puppeting bridge.
// Copyright (C) 2022 Sumner Evans, Karmanyaah Malhotra
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package database

import (
	"database/sql"
	"errors"
	"time"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"github.com/beeper/groupme-lib"
)

type MessageQuery struct {
	db  *Database
	log log.Logger
}

func (mq *MessageQuery) New() *Message {
	return &Message{
		db:  mq.db,
		log: mq.log,
	}
}

const (
	getAllMessagesSelect = `
		SELECT chat_gmid, chat_receiver, gmid, mxid, sender, timestamp, sent
		FROM messages
	`
	getAllMessagesQuery = getAllMessagesSelect + `
		WHERE chat_gmid=$1 AND chat_receiver=$2
	`
	getByGMIDQuery            = getAllMessagesQuery + "AND jid=$3"
	getByMXIDQuery            = getAllMessagesSelect + "WHERE mxid=$1"
	getLastMessageInChatQuery = getAllMessagesQuery + `
		AND timestamp<=$3 AND sent=true
		ORDER BY timestamp DESC
		LIMIT 1
	`
	getFirstMessageInChatQuery = getAllMessagesQuery + `
		AND sent=true
		ORDER BY timestamp ASC
		LIMIT 1
	`
	getMessagesBetweenQuery = getAllMessagesQuery + `
		AND timestamp>$3 AND timestamp<=$4 AND sent=true
		ORDER BY timestamp ASC
	`
)

func (mq *MessageQuery) GetAll(chat PortalKey) (messages []*Message) {
	rows, err := mq.db.Query(getAllMessagesQuery, chat.GMID, chat.Receiver)
	if err != nil || rows == nil {
		return nil
	}
	for rows.Next() {
		messages = append(messages, mq.New().Scan(rows))
	}
	return
}

func (mq *MessageQuery) GetByGMID(chat PortalKey, gmid groupme.ID) *Message {
	return mq.maybeScan(mq.db.QueryRow(getByGMIDQuery, chat.GMID, chat.Receiver, gmid))
}

func (mq *MessageQuery) GetByMXID(mxid id.EventID) *Message {
	return mq.maybeScan(mq.db.QueryRow(getByMXIDQuery, mxid))
}

func (mq *MessageQuery) GetLastInChat(chat PortalKey) *Message {
	return mq.GetLastInChatBefore(chat, time.Now().Add(60*time.Second))
}

func (mq *MessageQuery) GetLastInChatBefore(chat PortalKey, maxTimestamp time.Time) *Message {
	return mq.maybeScan(mq.db.QueryRow(getLastMessageInChatQuery, chat.GMID, chat.Receiver, maxTimestamp.Unix()))
}

func (mq *MessageQuery) GetFirstInChat(chat PortalKey) *Message {
	return mq.maybeScan(mq.db.QueryRow(getFirstMessageInChatQuery, chat.GMID, chat.Receiver))
}

func (mq *MessageQuery) GetMessagesBetween(chat PortalKey, minTimestamp, maxTimestamp time.Time) (messages []*Message) {
	rows, err := mq.db.Query(getMessagesBetweenQuery, chat.GMID, chat.Receiver, minTimestamp.Unix(), maxTimestamp.Unix())
	if err != nil || rows == nil {
		return nil
	}
	for rows.Next() {
		messages = append(messages, mq.New().Scan(rows))
	}
	return
}

func (mq *MessageQuery) maybeScan(row *sql.Row) *Message {
	if row == nil {
		return nil
	}
	return mq.New().Scan(row)
}

type Message struct {
	db  *Database
	log log.Logger

	Chat      PortalKey
	GMID      groupme.ID
	MXID      id.EventID
	Sender    groupme.ID
	Timestamp time.Time
	Sent      bool

	Portal Portal
}

func (msg *Message) Scan(row dbutil.Scannable) *Message {
	var ts int64
	err := row.Scan(&msg.Chat.GMID, &msg.Chat.Receiver, &msg.GMID, &msg.MXID, &msg.Sender, &ts, &msg.Sent)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			msg.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	if ts != 0 {
		msg.Timestamp = time.Unix(ts, 0)
	}
	return msg
}
