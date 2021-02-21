// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2020 Tulir Asokan
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
	"strings"
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix-whatsapp/types"
	"maunium.net/go/mautrix/id"
)

type UserQuery struct {
	db  *Database
	log log.Logger
}

func (uq *UserQuery) New() *User {
	return &User{
		db:  uq.db,
		log: uq.log,
	}
}

func (uq *UserQuery) GetAll() (users []*User) {
	ans := uq.db.Find(&users)
	if ans.Error != nil || len(users) == 0 {
		return nil
	}
	for _, i := range users {
		i.db = uq.db
		i.log = uq.log
	}
	return
}

func (uq *UserQuery) GetByMXID(userID id.UserID) *User {
	var user User
	ans := uq.db.Where("mxid = ?", userID).Take(&user)
	user.db = uq.db
	user.log = uq.log
	if ans.Error != nil {
		return nil
	}
	return &user
}

func (uq *UserQuery) GetByJID(userID types.GroupMeID) *User {
	var user User
	ans := uq.db.Where("jid = ?", userID).Limit(1).Find(&user)
	if ans.Error != nil || ans.RowsAffected == 0 {
		return nil
	}
	user.db = uq.db
	user.log = uq.log

	return &user
}

type User struct {
	db  *Database
	log log.Logger

	MXID  id.UserID       `gorm:"primaryKey"`
	JID   types.GroupMeID `gorm:"unique"`
	Token types.AuthToken

	ManagementRoom id.RoomID
	LastConnection uint64 `gorm:"notNull;default:0"`
}

//func (user *User) Scan(row Scannable) *User {
//	var jid, clientID, clientToken, serverToken sql.NullString
//	var encKey, macKey []byte
//	err := row.Scan(&user.MXID, &jid, &user.ManagementRoom, &user.LastConnection, &clientID, &clientToken, &serverToken, &encKey, &macKey)
//	if err != nil {
//		if err != sql.ErrNoRows {
//			user.log.Errorln("Database scan failed:", err)
//		}
//		return nil
//	}
//	if len(jid.String) > 0 && len(clientID.String) > 0 {
//		user.JID = jid.String + whatsappExt.NewUserSuffix
//		// user.Session = &whatsapp.Session{
//		// 	ClientId:    clientID.String,
//		// 	ClientToken: clientToken.String,
//		// 	ServerToken: serverToken.String,
//		// 	EncKey:      encKey,
//		// 	MacKey:      macKey,
//		// 	Wid:         jid.String + whatsappExt.OldUserSuffix,
//		// }
//	} // else {
//	// 	user.Session = nil
//	// }
//	return user
//}

func stripSuffix(jid types.GroupMeID) string {
	if len(jid) == 0 {
		return jid
	}

	index := strings.IndexRune(jid, '@')
	if index < 0 {
		return jid
	}

	return jid[:index]
}

func (user *User) jidPtr() *string {
	if len(user.JID) > 0 {
		str := stripSuffix(user.JID)
		return &str
	}
	return nil
}

//func (user *User) sessionUnptr() (sess whatsapp.Session) {
//	// if user.Session != nil {
//	// 	sess = *user.Session
//	// }
//	return
//}

func (user *User) Insert() {
	ans := user.db.Create(&user)
	if ans.Error != nil {
		user.log.Warnfln("Failed to insert %s: %v", user.MXID, ans.Error)
	}
}

func (user *User) UpdateLastConnection() {
	user.LastConnection = uint64(time.Now().Unix())
	user.Update()
}

func (user *User) Update() {
	ans := user.db.Save(&user)
	if ans.Error != nil {
		user.log.Warnfln("Failed to update last connection ts: %v", ans.Error)
	}
}

type PortalKeyWithMeta struct {
	PortalKey
	InCommunity bool
}

type UserPortal struct {
	UserJID types.GroupMeID `gorm:"primaryKey;"`

	PortalJID      types.GroupMeID `gorm:"primaryKey;"`
	PortalReceiver types.GroupMeID `gorm:"primaryKey;"`

	InCommunity bool `gorm:"notNull;default:false;"`

	User   User   `gorm:"foreignKey:UserJID;references:jid;constraint:OnDelete:CASCADE;"`
	Portal Portal `gorm:"foreignKey:PortalJID,PortalReceiver;references:JID,Receiver;constraint:OnDelete:CASCADE;"`
}

func (user *User) SetPortalKeys(newKeys []PortalKeyWithMeta) error {
	tx := user.db.Begin()
	ans := tx.Where("user_jid = ?", *user.jidPtr()).Delete(&UserPortal{})
	print("make sure all are deletede")
	if ans.Error != nil {
		_ = tx.Rollback()
		return ans.Error
	}

	for _, key := range newKeys {
		ans = tx.Create(&UserPortal{
			UserJID:        *user.jidPtr(),
			PortalJID:      key.JID,
			PortalReceiver: key.Receiver,
			InCommunity:    key.InCommunity,
		})
		if ans.Error != nil {
			_ = tx.Rollback()
			return ans.Error
		}
	}

	return tx.Commit().Error
}

func (user *User) IsInPortal(key PortalKey) bool {
	var count int64
	user.db.Find(&UserPortal{
		UserJID:        *user.jidPtr(),
		PortalJID:      key.JID,
		PortalReceiver: key.Receiver,
	}).Count(&count) //TODO: efficient
	return count > 0
}

func (user *User) GetPortalKeys() []PortalKey {
	var up []UserPortal
	ans := user.db.Where("user_jid = ?", *user.jidPtr()).Find(&up)
	if ans.Error != nil {
		user.log.Warnln("Failed to get user portal keys:", ans.Error)
		return nil
	}
	var keys []PortalKey
	for _, i := range up {
		key := PortalKey{
			JID:      i.UserJID,
			Receiver: i.PortalReceiver,
		}
		keys = append(keys, key)
	}
	return keys
}

func (user *User) GetInCommunityMap() map[PortalKey]bool {
	var up []UserPortal
	ans := user.db.Where("user_jid = ?", *user.jidPtr()).Find(&up)
	if ans.Error != nil {
		user.log.Warnln("Failed to get user portal keys:", ans.Error)
		return nil
	}
	keys := make(map[PortalKey]bool)
	for _, i := range up {
		key := PortalKey{
			JID:      i.PortalJID,
			Receiver: i.PortalReceiver,
		}
		keys[key] = i.InCommunity
	}
	return keys
}
