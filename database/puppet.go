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

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"github.com/karmanyaahm/groupme"
)

type PuppetQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PuppetQuery) New() *Puppet {
	return &Puppet{
		db:  pq.db,
		log: pq.log,

		EnableReceipts: true,
	}
}

const (
	puppetColumns                    = "gmid, displayname, name_set, avatar, avatar_url, avatar_set, custom_mxid, access_token, next_batch, enable_receipts"
	getAllPuppetsQuery               = "SELECT " + puppetColumns + " FROM puppets"
	getPuppetQuery                   = getAllPuppetsQuery + " WHERE gmid=$1"
	getPuppetByCustomMXIDQuery       = getAllPuppetsQuery + " WHERE custom_mxid=$1"
	getAllPuppetsWithCustomMXIDQuery = getAllPuppetsQuery + " WHERE custom_mxid<>''"
)

func (pq *PuppetQuery) GetAll() (puppets []*Puppet) {
	rows, err := pq.db.Query(getAllPuppetsQuery)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		puppets = append(puppets, pq.New().Scan(rows))
	}
	return
}

func (pq *PuppetQuery) Get(gmid groupme.ID) *Puppet {
	row := pq.db.QueryRow(getPuppetQuery, gmid)
	if row == nil {
		return nil
	}
	return pq.New().Scan(row)
}

func (pq *PuppetQuery) GetByCustomMXID(mxid id.UserID) *Puppet {
	row := pq.db.QueryRow(getPuppetByCustomMXIDQuery, mxid)
	if row == nil {
		return nil
	}
	return pq.New().Scan(row)
}

func (pq *PuppetQuery) GetAllWithCustomMXID() (puppets []*Puppet) {
	rows, err := pq.db.Query(getAllPuppetsWithCustomMXIDQuery)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		puppets = append(puppets, pq.New().Scan(rows))
	}
	return
}

// Puppet is comment
type Puppet struct {
	db  *Database
	log log.Logger

	GMID groupme.ID

	Displayname string
	NameSet     bool

	Avatar    string
	AvatarURL id.ContentURI
	AvatarSet bool

	CustomMXID     id.UserID
	AccessToken    string
	NextBatch      string
	EnableReceipts bool
}

func (puppet *Puppet) Scan(row dbutil.Scannable) *Puppet {
	var displayname, avatar, avatarURL, customMXID, accessToken, nextBatch sql.NullString
	var enableReceipts, nameSet, avatarSet sql.NullBool
	var gmid string
	err := row.Scan(&gmid, &displayname, &nameSet, &avatar, &avatarURL, &avatarSet, &customMXID, &accessToken, &nextBatch, &enableReceipts)
	if err != nil {
		if err != sql.ErrNoRows {
			puppet.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	puppet.GMID = groupme.ID(gmid)
	puppet.Displayname = displayname.String
	puppet.NameSet = nameSet.Bool
	puppet.Avatar = avatar.String
	puppet.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
	puppet.AvatarSet = avatarSet.Bool
	puppet.CustomMXID = id.UserID(customMXID.String)
	puppet.AccessToken = accessToken.String
	puppet.NextBatch = nextBatch.String
	puppet.EnableReceipts = enableReceipts.Bool
	return puppet
}

func (puppet *Puppet) Insert() {
	_, err := puppet.db.Exec(`
		INSERT INTO puppet (username, avatar, avatar_url, avatar_set, displayname, name_set,
		                    custom_mxid, access_token, next_batch, enable_receipts)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, puppet.GMID, puppet.Avatar, puppet.AvatarURL.String(), puppet.AvatarSet, puppet.Displayname,
		puppet.NameSet, puppet.CustomMXID, puppet.AccessToken, puppet.NextBatch,
		puppet.EnableReceipts,
	)
	if err != nil {
		puppet.log.Warnfln("Failed to insert %s: %v", puppet.GMID, err)
	}
}

func (puppet *Puppet) Update() {
	_, err := puppet.db.Exec(`
		UPDATE puppet
		SET displayname=$1, name_set=$2, avatar=$3, avatar_url=$4, avatar_set=$5, custom_mxid=$6,
		access_token=$7, next_batch=$8, enable_receipts=$10
		WHERE username=$11
	`, puppet.Displayname, puppet.NameSet, puppet.Avatar, puppet.AvatarURL.String(), puppet.AvatarSet,
		puppet.CustomMXID, puppet.AccessToken, puppet.NextBatch, puppet.EnableReceipts,
		puppet.GMID)
	if err != nil {
		puppet.log.Warnfln("Failed to update %s: %v", puppet.GMID, err)
	}
}
