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
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"

	"github.com/karmanyaahm/matrix-groupme-go/types"
)

type PuppetQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PuppetQuery) New() *Puppet {
	return &Puppet{
		db:  pq.db,
		log: pq.log,

		EnablePresence: true,
		EnableReceipts: true,
	}
}

func (pq *PuppetQuery) GetAll() (puppets []*Puppet) {
	ans := pq.db.Find(&puppets)
	if ans.Error != nil || len(puppets) == 0 {
		return nil
	}
	for _, puppet := range puppets {
		pq.initializePuppet(puppet)
	}
	// defer rows.Close()
	// for rows.Next() {
	// 	puppets = append(puppets, pq.New().Scan(rows))
	// }
	return
}

func (pq *PuppetQuery) Get(jid types.GroupMeID) *Puppet {
	puppet := Puppet{}
	ans := pq.db.Where("jid = ?", jid).Limit(1).Find(&puppet)
	if ans.Error != nil || ans.RowsAffected == 0 {
		return nil
	}
	pq.initializePuppet(&puppet)
	return &puppet
}

func (pq *PuppetQuery) GetByCustomMXID(mxid id.UserID) *Puppet {
	puppet := Puppet{}
	ans := pq.db.Where("custom_mxid = ?", mxid).Limit(1).Find(&puppet)
	if ans.Error != nil || ans.RowsAffected == 0 {
		return nil
	}
	pq.initializePuppet(&puppet)
	return &puppet
}

func (pq *PuppetQuery) GetAllWithCustomMXID() (puppets []*Puppet) {

	ans := pq.db.Find(&puppets, "custom_mxid <> ''")
	if ans.Error != nil || len(puppets) != 0 {
		return nil
	}
	for _, puppet := range puppets {
		pq.initializePuppet(puppet)
	}
	// defer rows.Close()
	// for rows.Next() {
	// 	puppets = append(puppets, pq.New().Scan(rows))
	// }
	return
}

func (pq *PuppetQuery) initializePuppet(p *Puppet) {
	p.db = pq.db
	p.log = pq.log
}

//Puppet is comment
type Puppet struct {
	db  *Database
	log log.Logger

	JID types.GroupMeID `gorm:"primaryKey"`
	//Avatar      string
	//AvatarURL   types.ContentURI
	//Displayname string
	//NameQuality int8

	CustomMXID     id.UserID `gorm:"column:custom_mxid;"`
	AccessToken    string
	NextBatch      string
	EnablePresence bool `gorm:"notNull;default:true"`
	EnableReceipts bool `gorm:"notNull;default:true"`
}

// func (puppet *Puppet) Scan(row Scannable) *Puppet {
// 	var displayname, avatar, avatarURL, customMXID, accessToken, nextBatch sql.NullString
// 	var quality sql.NullInt64
// 	var enablePresence, enableReceipts sql.NullBool
// 	err := row.Scan(&puppet.JID, &avatar, &avatarURL, &displayname, &quality, &customMXID, &accessToken, &nextBatch, &enablePresence, &enableReceipts)
// 	if err != nil {
// 		if err != sql.ErrNoRows {
// 			puppet.log.Errorln("Database scan failed:", err)
// 		}
// 		return nil
// 	}
// 	puppet.Displayname = displayname.String
// 	puppet.Avatar = avatar.String
// 	puppet.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
// 	puppet.NameQuality = int8(quality.Int64)
// 	puppet.CustomMXID = id.UserID(customMXID.String)
// 	puppet.AccessToken = accessToken.String
// 	puppet.NextBatch = nextBatch.String
// 	puppet.EnablePresence = enablePresence.Bool
// 	puppet.EnableReceipts = enableReceipts.Bool
// 	return puppet
// }

func (puppet *Puppet) Insert() {
	// _, err := puppet.db.Exec("INSERT INTO puppet (jid, avatar, avatar_url, displayname, name_quality, custom_mxid, access_token, next_batch, enable_presence, enable_receipts) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)",
	// 	puppet.JID, puppet.Avatar, puppet.AvatarURL.String(), puppet.Displayname, puppet.NameQuality, puppet.CustomMXID, puppet.AccessToken, puppet.NextBatch, puppet.EnablePresence, puppet.EnableReceipts)
	ans := puppet.db.Create(&puppet)
	if ans.Error != nil {
		puppet.log.Warnfln("Failed to insert %s: %v", puppet.JID, ans.Error)
	}
}

func (puppet *Puppet) Update() {
	ans := puppet.db.Where("jid = ?", puppet.JID).Updates(&puppet)
	if ans.Error != nil {
		puppet.log.Warnfln("Failed to update %s->%s: %v", puppet.JID, ans.Error)
	}
}
