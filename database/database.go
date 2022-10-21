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
	"errors"
	"net"

	"github.com/lib/pq"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	"maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/util/dbutil"

	"github.com/beeper/groupme/database/upgrades"
)

type Database struct {
	*dbutil.Database

	User     *UserQuery
	Portal   *PortalQuery
	Puppet   *PuppetQuery
	Message  *MessageQuery
	Reaction *ReactionQuery
}

func New(baseDB *dbutil.Database, log maulogger.Logger) *Database {
	db := &Database{Database: baseDB}
	db.UpgradeTable = upgrades.Table
	db.User = &UserQuery{
		db:  db,
		log: log.Sub("User"),
	}
	db.Portal = &PortalQuery{
		db:  db,
		log: log.Sub("Portal"),
	}
	db.Puppet = &PuppetQuery{
		db:  db,
		log: log.Sub("Puppet"),
	}
	db.Message = &MessageQuery{
		db:  db,
		log: log.Sub("Message"),
	}
	db.Reaction = &ReactionQuery{
		db:  db,
		log: log.Sub("Reaction"),
	}
	return db
}

func isRetryableError(err error) bool {
	if pqError := (&pq.Error{}); errors.As(err, &pqError) {
		switch pqError.Code.Class() {
		case "08", // Connection Exception
			"53", // Insufficient Resources (e.g. too many connections)
			"57": // Operator Intervention (e.g. server restart)
			return true
		}
	} else if netError := (&net.OpError{}); errors.As(err, &netError) {
		return true
	}
	return false
}
