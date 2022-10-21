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
	log "maunium.net/go/maulogger/v2"

	"github.com/karmanyaahm/matrix-groupme-go/groupmeExt"
	"github.com/karmanyaahm/matrix-groupme-go/types"
	"maunium.net/go/mautrix/id"
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

func (mq *MessageQuery) GetAll(chat PortalKey) (messages []*Message) {
	ans := mq.db.Where("chat_jid = ? AND chat_receiver = ?", chat.JID, chat.Receiver).Find(&messages)
	if ans.Error != nil || len(messages) == 0 {
		return nil
	}
	return
}

func (mq *MessageQuery) GetByJID(chat PortalKey, jid types.WhatsAppMessageID) *Message {
	var message Message
	ans := mq.db.Where("chat_jid = ? AND chat_receiver = ? AND jid = ?", chat.JID, chat.Receiver, jid).Limit(1).Find(&message)
	if ans.Error != nil || ans.RowsAffected == 0 {
		return nil
	}
	return &message
}

func (mq *MessageQuery) GetByMXID(mxid id.EventID) *Message {
	var message Message
	ans := mq.db.Where("mxid = ?", mxid).Limit(1).Find(&message)
	if ans.Error != nil || ans.RowsAffected == 0 {
		return nil
	}
	return &message
}

func (mq *MessageQuery) GetLastInChat(chat PortalKey) *Message {
	var message Message
	ans := mq.db.Where("chat_jid = ? AND chat_receiver = ?", chat.JID, chat.Receiver).Order("timestamp desc").Limit(1).Find(&message)
	if ans.Error != nil || ans.RowsAffected == 0 {
		return nil
	}
	return &message

}

type Message struct {
	db  *Database
	log log.Logger

	Chat      PortalKey           `gorm:"embedded;embeddedPrefix:chat_"`
	JID       types.GroupMeID     `gorm:"primaryKey;unique;notNull"`
	MXID      id.EventID          `gorm:"primaryKey;unique;notNull"`
	Sender    types.GroupMeID     `gorm:"notNull"`
	Timestamp uint64              `gorm:"notNull;default:0"`
	Content   *groupmeExt.Message `gorm:"type:TEXT;notNull"`

	Portal Portal `gorm:"foreignKey:chat_jid,chat_receiver;references:jid,receiver;constraint:onDelete:CASCADE;"`
}

// func (msg *Message) Scan(row Scannable) *Message {
// 	var content []byte
// 	err := row.Scan(&msg.Chat.JID, &msg.Chat.Receiver, &msg.JID, &msg.MXID, &msg.Sender, &msg.Timestamp, &content)
// 	if err != nil {
// 		if err != sql.ErrNoRows {
// 			msg.log.Errorln("Database scan failed:", err)
// 		}
// 		return nil
// 	}

// 	msg.decodeBinaryContent(content)

// 	return msg
// }

// func (msg *Message) decodeBinaryContent(content []byte) {
// 	msg.Content = &waProto.Message{}
// 	reader := bytes.NewReader(content)
// 	dec := json.NewDecoder(reader)
// 	err := dec.Decode(msg.Content)
// 	if err != nil {
// 		msg.log.Warnln("Failed to decode message content:", err)
// 	}
// }

// func (msg *Message) encodeBinaryContent() []byte {
// 	var buf bytes.Buffer
// 	enc := json.NewEncoder(&buf)
// 	err := enc.Encode(msg.Content)
// 	if err != nil {
// 		msg.log.Warnln("Failed to encode message content:", err)
// 	}
// 	return buf.Bytes()
// }

func (msg *Message) Insert() {
	ans := msg.db.Create(&msg)
	if ans.Error != nil {
		msg.log.Warnfln("Failed to insert %s@%s: %v", msg.Chat, msg.JID, ans.Error)
	}
}

func (msg *Message) Delete() {
	ans := msg.db.Delete(&msg)
	if ans.Error != nil {
		msg.log.Warnfln("Failed to delete %s@%s: %v", msg.Chat, msg.JID, ans.Error)
	}
}
