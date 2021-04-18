// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2019 Tulir Asokan
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
	"os"
	"strings"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	log "maunium.net/go/maulogger/v2"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type Database struct {
	*gorm.DB
	log     log.Logger
	dialect string

	User     *UserQuery
	Portal   *PortalQuery
	Puppet   *PuppetQuery
	Message  *MessageQuery
	Reaction *ReactionQuery
}

func New(dbType string, uri string, baseLog log.Logger) (*Database, error) {

	var conn gorm.Dialector

	if dbType == "sqlite3" {
		//_, _ = conn.Exec("PRAGMA foreign_keys = ON")
		log.Fatalln("no sqlite for now only postgresql")
		os.Exit(1)
		conn = sqlite.Open(uri)
	} else {
		conn = postgres.Open(uri)
	}

	gdb, err := gorm.Open(conn, &gorm.Config{
		//Logger: logger.Default.LogMode(logger.Info),
		// Logger: baseLog,

		DisableForeignKeyConstraintWhenMigrating: true,
		NamingStrategy: schema.NamingStrategy{
			NameReplacer: strings.NewReplacer("JID", "Jid", "MXID", "Mxid"),
		},
	})
	if err != nil {
		panic("failed to connect database")
	}
	db := &Database{
		DB:      gdb,
		log:     baseLog.Sub("Database"),
		dialect: dbType,
	}
	db.User = &UserQuery{
		db:  db,
		log: db.log.Sub("User"),
	}
	db.Portal = &PortalQuery{
		db:  db,
		log: db.log.Sub("Portal"),
	}
	db.Puppet = &PuppetQuery{
		db:  db,
		log: db.log.Sub("Puppet"),
	}
	db.Message = &MessageQuery{
		db:  db,
		log: db.log.Sub("Message"),
	}
	db.Reaction = &ReactionQuery{
		db:  db,
		log: db.log.Sub("Reaction"),
	}

	return db, nil
}

func (db *Database) Init() error {
	println("actual upgrade")
	err := db.AutoMigrate(&Portal{}, &Puppet{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&Message{})
	if err != nil {
		return err
	}

	err = db.AutoMigrate(&Reaction{})
	if err != nil {
		return err
	}

	err = db.AutoMigrate(&mxRegistered{}, &MxUserProfile{})
	if err != nil {
		return err
	}

	err = db.AutoMigrate(&User{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&UserPortal{})
	if err != nil {
		return err
	}
	return nil //upgrades.Run(db.log.Sub("Upgrade"), db.dialect, db.DB)
}

type Scannable interface {
	Scan(...interface{}) error
}
