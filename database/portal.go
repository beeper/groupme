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
	"fmt"
	"strconv"
	"strings"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"github.com/beeper/groupme-lib"
)

// GMID is the puppet or the group
// Receiver is the "Other Person" in a DM or the group itself in a group
type PortalKey struct {
	GMID     groupme.ID
	Receiver groupme.ID
}

func ParsePortalKey(inp string) *PortalKey {
	parts := strings.Split(inp, "+")

	if len(parts) == 1 {
		if i, err := strconv.Atoi(inp); i == 0 || err != nil {
			return nil
		}
		return &PortalKey{groupme.ID(inp), groupme.ID(inp)}
	} else if len(parts) == 2 {
		if i, err := strconv.Atoi(parts[0]); i == 0 || err != nil {
			return nil
		}
		if i, err := strconv.Atoi(parts[1]); i == 0 || err != nil {
			return nil
		}

		return &PortalKey{
			GMID:     groupme.ID(parts[1]),
			Receiver: groupme.ID(parts[0]),
		}
	} else {
		return nil
	}
}

func GroupPortalKey(gmid groupme.ID) PortalKey {
	return PortalKey{
		GMID:     gmid,
		Receiver: gmid,
	}
}

func NewPortalKey(gmid, receiver groupme.ID) PortalKey {
	return PortalKey{
		GMID:     gmid,
		Receiver: receiver,
	}
}

func (key PortalKey) String() string {
	if key.Receiver == key.GMID {
		return key.GMID.String()
	}
	return key.GMID.String() + "+" + key.Receiver.String()
}

func (key PortalKey) IsPrivate() bool {
	//also see FindPrivateChats
	return key.GMID != key.Receiver
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

const (
	portalColumns        = "gmid, receiver, mxid, name, name_set, topic, topic_set, avatar, avatar_url, avatar_set, encrypted"
	getAllPortalsQuery   = "SELECT " + portalColumns + " FROM portal"
	getPortalByGMIDQuery = getAllPortalsQuery + " WHERE gmid=$1 AND receiver=$2"
	getPortalByMXIDQuery = getAllPortalsQuery + " WHERE mxid=$1"
	getAllPortalsByGMID  = getAllPortalsQuery + " WHERE gmid=$1"
	getAllPrivateChats   = getAllPortalsQuery + " WHERE receiver=$1 AND receiver <> gmid"
)

func (pq *PortalQuery) GetAll() []*Portal {
	return pq.getAll(getAllPortalsQuery)
}

func (pq *PortalQuery) GetByGMID(key PortalKey) *Portal {
	return pq.get(getPortalByGMIDQuery, key.GMID, key.Receiver)
}

func (pq *PortalQuery) GetByMXID(mxid id.RoomID) *Portal {
	return pq.get(getPortalByMXIDQuery, mxid)
}

func (pq *PortalQuery) GetAllByGMID(gmid groupme.ID) []*Portal {
	return pq.getAll(getAllPortalsByGMID, gmid)
}

func (pq *PortalQuery) FindPrivateChats(receiver groupme.ID) []*Portal {
	return pq.getAll(getAllPrivateChats, receiver)
}

func (pq *PortalQuery) getAll(query string, args ...any) (portals []*Portal) {
	rows, err := pq.db.Query(query, args...)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		portals = append(portals, pq.New().Scan(rows))
	}
	return
}

func (pq *PortalQuery) get(query string, args ...interface{}) *Portal {
	row := pq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}
	return pq.New().Scan(row)
}

type Portal struct {
	db  *Database
	log log.Logger

	Key  PortalKey
	MXID id.RoomID

	Name      string
	NameSet   bool
	Topic     string
	TopicSet  bool
	Avatar    string
	AvatarURL id.ContentURI
	AvatarSet bool
	Encrypted bool
}

func (portal *Portal) Scan(row dbutil.Scannable) *Portal {
	var mxid, avatarURL sql.NullString

	err := row.Scan(&portal.Key.GMID, &portal.Key.Receiver, &mxid, &portal.Name, &portal.NameSet, &portal.Topic, &portal.TopicSet, &portal.Avatar, &avatarURL, &portal.AvatarSet, &portal.Encrypted)
	if err != nil {
		if err != sql.ErrNoRows {
			portal.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	portal.MXID = id.RoomID(mxid.String)
	portal.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
	return portal
}

func (portal *Portal) mxidPtr() *id.RoomID {
	if len(portal.MXID) > 0 {
		return &portal.MXID
	}
	return nil
}

func (portal *Portal) Insert() {
	_, err := portal.db.Exec(fmt.Sprintf(`
		INSERT INTO portal (%s)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, portalColumns),
		portal.Key.GMID, portal.Key.Receiver, portal.mxidPtr(), portal.Name, portal.NameSet, portal.Topic, portal.TopicSet, portal.Avatar, portal.AvatarURL.String(), portal.AvatarSet, portal.Encrypted)
	if err != nil {
		portal.log.Warnfln("Failed to insert %s: %v", portal.Key, err)
	}
}

func (portal *Portal) Update(txn dbutil.Transaction) {
	query := `
		UPDATE portal
		SET mxid=$1, name=$2, name_set=$3, topic=$4, topic_set=$5, avatar=$6, avatar_url=$7, avatar_set=$8, encrypted=$9
		WHERE gmid=$10 AND receiver=$11
	`
	args := []interface{}{
		portal.mxidPtr(), portal.Name, portal.NameSet, portal.Topic, portal.TopicSet, portal.Avatar, portal.AvatarURL.String(),
		portal.AvatarSet, portal.Encrypted, portal.Key.GMID, portal.Key.Receiver,
	}
	var err error
	if txn != nil {
		_, err = txn.Exec(query, args...)
	} else {
		_, err = portal.db.Exec(query, args...)
	}
	if err != nil {
		portal.log.Warnfln("Failed to update %s: %v", portal.Key, err)
	}
}

func (portal *Portal) Delete() {
	_, err := portal.db.Exec("DELETE FROM portal WHERE gmid=$1 AND receiver=$2", portal.Key.GMID, portal.Key.Receiver)
	if err != nil {
		portal.log.Warnfln("Failed to delete %s: %v", portal.Key, err)
	}
}
