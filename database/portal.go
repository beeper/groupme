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
	"gorm.io/gorm"
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"

	"github.com/karmanyaahm/matrix-groupme-go/types"
)

// JID is the puppet or the group
// Receiver is the "Other Person" in a DM or the group itself in a group
type PortalKey struct {
	JID      types.GroupMeID `gorm:"primaryKey"`
	Receiver types.GroupMeID `gorm:"primaryKey"`
}

func GroupPortalKey(jid types.GroupMeID) PortalKey {
	return PortalKey{
		JID:      jid,
		Receiver: jid,
	}
}

func NewPortalKey(jid, receiver types.GroupMeID) PortalKey {
	return PortalKey{
		JID:      jid,
		Receiver: receiver,
	}
}

func (key PortalKey) String() string {
	if key.Receiver == key.JID {
		return key.JID
	}
	return key.JID + "-" + key.Receiver
}

func (key PortalKey) IsPrivate() bool {
	//also see FindPrivateChats
	return key.JID != key.Receiver
}

type PortalQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PortalQuery) New() *Portal {
	return &Portal{
		db:  pq.db,
		log: pq.log,
	}
}

func (pq *PortalQuery) GetAll() []*Portal {
	return pq.getAll(pq.db.DB)

}

func (pq *PortalQuery) GetByJID(key PortalKey) *Portal {
	return pq.get(pq.db.DB.Where("jid = ? AND receiver = ?", key.JID, key.Receiver))

}

func (pq *PortalQuery) GetByMXID(mxid id.RoomID) *Portal {
	return pq.get(pq.db.DB.Where("mxid = ?", mxid))
}

func (pq *PortalQuery) GetAllByJID(jid types.GroupMeID) []*Portal {
	return pq.getAll(pq.db.DB.Where("jid = ?", jid))

}

func (pq *PortalQuery) FindPrivateChats(receiver types.GroupMeID) []*Portal {
	//also see IsPrivate
	return pq.getAll(pq.db.DB.Where("receiver = ? AND receiver <> jid", receiver))

}

func (pq *PortalQuery) getAll(db *gorm.DB) (portals []*Portal) {
	ans := db.Find(&portals)
	if ans.Error != nil || len(portals) == 0 {
		return nil
	}
	for _, i := range portals {
		i.db = pq.db
		i.log = pq.log
	}
	return

}

func (pq *PortalQuery) get(db *gorm.DB) *Portal {
	var portal Portal
	ans := db.Limit(1).Find(&portal)
	if ans.Error != nil || db.RowsAffected == 0 {
		return nil
	}
	portal.db = pq.db
	portal.log = pq.log

	return &portal
}

type Portal struct {
	db  *Database
	log log.Logger

	Key  PortalKey `gorm:"primaryKey;embedded"`
	MXID id.RoomID

	Name      string
	Topic     string
	Avatar    string
	AvatarURL types.ContentURI
	Encrypted bool `gorm:"notNull;default:false"`
}

// func (portal *Portal) Scan(row Scannable) *Portal {
// 	var mxid, avatarURL sql.NullString
// 	err := row.Scan(&portal.Key.JID, &portal.Key.Receiver, &mxid, &portal.Name, &portal.Topic, &portal.Avatar, &avatarURL, &portal.Encrypted)
// 	if err != nil {
// 		if err != sql.ErrNoRows {
// 			portal.log.Errorln("Database scan failed:", err)
// 		}
// 		return nil
// 	}
// 	portal.MXID = id.RoomID(mxid.String)
// 	portal.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
// 	return portal
// }

func (portal *Portal) mxidPtr() *id.RoomID {
	if len(portal.MXID) > 0 {
		return &portal.MXID
	}
	return nil
}

func (portal *Portal) Insert() {

	ans := portal.db.Create(&portal)
	print("beware of types")
	if ans.Error != nil {
		portal.log.Warnfln("Failed to insert %s: %v", portal.Key, ans.Error)
	}
}

func (portal *Portal) Update() {
	ans := portal.db.Where("jid = ? AND receiver = ?", portal.Key.JID, portal.Key.Receiver).Save(&portal)
	print("check .model vs not")

	if ans.Error != nil {
		portal.log.Warnfln("Failed to update %s: %v", portal.Key, ans.Error)
	}
}

func (portal *Portal) Delete() {
	ans := portal.db.Where("jid = ? AND receiver = ?", portal.Key.JID, portal.Key.Receiver).Delete(&portal)
	if ans.Error != nil {
		portal.log.Warnfln("Failed to delete %s: %v", portal.Key, ans.Error)
	}
}

func (portal *Portal) GetUserIDs() []id.UserID {
	//TODO: gorm this
	rows, err := portal.db.Raw(`SELECT "users".mxid FROM "users", user_portals
		WHERE "users".jid=user_portals.user_jid
			AND user_portals.portal_jid = ?
			AND user_portals.portal_receiver = ?`,
		portal.Key.JID, portal.Key.Receiver).Rows()
	if err != nil {
		portal.log.Debugln("Failed to get portal user ids:", err)
		return nil
	}
	var userIDs []id.UserID
	for rows.Next() {
		var userID id.UserID
		err = rows.Scan(&userID)
		if err != nil {
			portal.log.Warnln("Failed to scan row:", err)
			continue
		}
		userIDs = append(userIDs, userID)
	}
	return userIDs
}
