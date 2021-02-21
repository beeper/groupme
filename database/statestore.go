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
	"errors"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type SQLStateStore struct {
	*appservice.TypingStateStore

	db  *Database
	log log.Logger

	Typing     map[id.RoomID]map[id.UserID]int64
	typingLock sync.RWMutex
}

type mxRegistered struct {
	UserID string `gorm:"primaryKey"`
}

var _ appservice.StateStore = (*SQLStateStore)(nil)

func NewSQLStateStore(db *Database) *SQLStateStore {
	return &SQLStateStore{
		TypingStateStore: appservice.NewTypingStateStore(),
		db:               db,
		log:              db.log.Sub("StateStore"),
	}
}

func (store *SQLStateStore) IsRegistered(userID id.UserID) bool {
	v := mxRegistered{UserID: userID.String()}
	var count int64
	ans := store.db.Model(&mxRegistered{}).Where(&v).Count(&count)

	if errors.Is(ans.Error, gorm.ErrRecordNotFound) {
		return false
	}
	if ans.Error != nil {
		store.log.Warnfln("Failed to scan registration existence for %s: %v", userID, ans.Error)
	}
	return count >= 1
}

func (store *SQLStateStore) MarkRegistered(userID id.UserID) {

	ans := store.db.Create(mxRegistered{userID.String()})

	if ans.Error != nil {
		store.log.Warnfln("Failed to mark %s as registered: %v", userID, ans.Error)
	}
}

type mxUserProfile struct {
	RoomID     string `gorm:"primaryKey"`
	UserID     string `gorm:"primaryKey"`
	Membership string `gorm:"notNull"`

	DisplayName string
	AvatarURL   string
}

func (store *SQLStateStore) GetRoomMembers(roomID id.RoomID) map[id.UserID]*event.MemberEventContent {
	members := make(map[id.UserID]*event.MemberEventContent)
	var users []mxUserProfile
	ans := store.db.Where("room_id = ?", roomID.String()).Find(&users)
	if ans.Error != nil {
		return members
	}

	var userID id.UserID
	var member event.MemberEventContent
	for _, user := range users {
		// if err != nil {
		// 	store.log.Warnfln("Failed to scan member in %s: %v", roomID, err)
		// 	continue
		// }
		userID = id.UserID(user.UserID)
		member = event.MemberEventContent{
			Membership:  event.Membership(user.Membership),
			Displayname: user.DisplayName,
			AvatarURL:   id.ContentURIString(user.AvatarURL),
		}

		members[userID] = &member
	}
	return members
}

func (store *SQLStateStore) GetMembership(roomID id.RoomID, userID id.UserID) event.Membership {
	var user mxUserProfile
	ans := store.db.Where("room_id = ? AND user_id = ?", roomID, userID).Limit(1).Find(&user)
	membership := event.MembershipLeave
	if ans.Error != nil && ans.Error != gorm.ErrRecordNotFound {
		store.log.Warnfln("Failed to scan membership of %s in %s: %v", userID, roomID, ans.Error)
	}
	membership = event.Membership(user.Membership)

	return membership
}

func (store *SQLStateStore) GetMember(roomID id.RoomID, userID id.UserID) *event.MemberEventContent {
	member, ok := store.TryGetMember(roomID, userID)
	if !ok {
		member.Membership = event.MembershipLeave
	}
	return member
}

func (store *SQLStateStore) TryGetMember(roomID id.RoomID, userID id.UserID) (*event.MemberEventContent, bool) {
	var user mxUserProfile
	ans := store.db.Where("room_id = ? AND user_id = ?", roomID, userID).Take(&user)

	if ans.Error != nil && ans.Error != gorm.ErrRecordNotFound {
		store.log.Warnfln("Failed to scan member info of %s in %s: %v", userID, roomID, ans.Error)
	}
	eventMember := event.MemberEventContent{
		Membership:  event.Membership(user.Membership),
		Displayname: user.DisplayName,
		AvatarURL:   id.ContentURIString(user.AvatarURL),
	}

	return &eventMember, ans.Error != nil
}

func (store *SQLStateStore) FindSharedRooms(userID id.UserID) (rooms []id.RoomID) {

	rows, err := store.db.Table("mx_user_profile").Select("room_id").
		Joins("LEFT JOIN portal ON portal.mxid=mx_user_profile.room_id").
		Where("user_id = ? AND portal.encrypted=true", userID).Rows()
	if err != nil {
		store.log.Warnfln("Failed to query shared rooms with %s: %v", userID, err)
		return
	}
	print("running maybe maybe code f937060306")
	for rows.Next() {
		var roomID id.RoomID
		err := rows.Scan(&roomID)
		if err != nil {
			store.log.Warnfln("Failed to scan room ID: %v", err)
		} else {
			rooms = append(rooms, roomID)
		}
	}
	return
}

func (store *SQLStateStore) IsInRoom(roomID id.RoomID, userID id.UserID) bool {
	return store.IsMembership(roomID, userID, "join")
}

func (store *SQLStateStore) IsInvited(roomID id.RoomID, userID id.UserID) bool {
	return store.IsMembership(roomID, userID, "join", "invite")
}

func (store *SQLStateStore) IsMembership(roomID id.RoomID, userID id.UserID, allowedMemberships ...event.Membership) bool {
	membership := store.GetMembership(roomID, userID)
	for _, allowedMembership := range allowedMemberships {
		if allowedMembership == membership {
			return true
		}
	}
	return false
}

func (store *SQLStateStore) SetMembership(roomID id.RoomID, userID id.UserID, membership event.Membership) {
	var err error
	user := mxUserProfile{
		RoomID:     roomID.String(),
		UserID:     userID.String(),
		Membership: string(membership),
	}
	print("weird thing 2 502650285")
	print(user.Membership)

	ans := store.db.Debug().Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "room_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"membership"}),
	}).Create(&user)

	if ans.Error != nil {
		store.log.Warnfln("Failed to set membership of %s in %s to %s: %v", userID, roomID, membership, err)
	}
}

func (store *SQLStateStore) SetMember(roomID id.RoomID, userID id.UserID, member *event.MemberEventContent) {

	user := mxUserProfile{
		RoomID:      roomID.String(),
		UserID:      userID.String(),
		Membership:  string(member.Membership),
		DisplayName: member.Displayname,
		AvatarURL:   string(member.AvatarURL),
	}
	ans := store.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "room_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"membership"}),
	}).Create(&user)

	if ans.Error != nil {
		store.log.Warnfln("Failed to set membership of %s in %s to %s: %v", userID, roomID, member, ans.Error)
	}
}

func (store *SQLStateStore) SetPowerLevels(roomID id.RoomID, levels *event.PowerLevelsEventContent) {
	// levelsBytes, err := json.Marshal(levels)
	// if err != nil {
	// 	store.log.Errorfln("Failed to marshal power levels of %s: %v", roomID, err)
	// 	return
	// }
	// if store.db.dialect == "postgres" {
	// 	_, err = store.db.Exec(`INSERT INTO mx_room_state (room_id, power_levels) VALUES ($1, $2)
	// 		ON CONFLICT (room_id) DO UPDATE SET power_levels=$2`, roomID, levelsBytes)
	// } else if store.db.dialect == "sqlite3" {
	// 	_, err = store.db.Exec("INSERT OR REPLACE INTO mx_room_state (room_id, power_levels) VALUES ($1, $2)", roomID, levelsBytes)
	// } else {
	// 	err = fmt.Errorf("unsupported dialect %s", store.db.dialect)
	// }
	// if err != nil {
	// 	store.log.Warnfln("Failed to store power levels of %s: %v", roomID, err)
	// }
}

func (store *SQLStateStore) GetPowerLevels(roomID id.RoomID) (levels *event.PowerLevelsEventContent) {
	// row := store.db.QueryRow("SELECT power_levels FROM mx_room_state WHERE room_id=$1", roomID)
	// if row == nil {
	// 	return
	// }
	// var data []byte
	// err := row.Scan(&data)
	// if err != nil {
	// 	store.log.Errorln("Failed to scan power levels of %s: %v", roomID, err)
	// 	return
	// }
	// levels = &event.PowerLevelsEventContent{}
	// err = json.Unmarshal(data, levels)
	// if err != nil {
	// 	store.log.Errorln("Failed to parse power levels of %s: %v", roomID, err)
	// 	return nil
	// }
	return
}

func (store *SQLStateStore) GetPowerLevel(roomID id.RoomID, userID id.UserID) int {
	// if store.db.dialect == "postgres" {
	// 	row := store.db.QueryRow(`SELECT
	// 		COALESCE((power_levels->'users'->$2)::int, (power_levels->'users_default')::int, 0)
	// 		FROM mx_room_state WHERE room_id=$1`, roomID, userID)
	// 	if row == nil {
	// 		// Power levels not in db
	// 		return 0
	// 	}
	// 	var powerLevel int
	// 	err := row.Scan(&powerLevel)
	// 	if err != nil {
	// 		store.log.Errorln("Failed to scan power level of %s in %s: %v", userID, roomID, err)
	// 	}
	// 	return powerLevel
	// }
	return store.GetPowerLevels(roomID).GetUserLevel(userID)
}

func (store *SQLStateStore) GetPowerLevelRequirement(roomID id.RoomID, eventType event.Type) int {
	// if store.db.dialect == "postgres" {
	// 	defaultType := "events_default"
	// 	defaultValue := 0
	// 	if eventType.IsState() {
	// 		defaultType = "state_default"
	// 		defaultValue = 50
	// 	}
	// 	row := store.db.QueryRow(`SELECT
	// 		COALESCE((power_levels->'events'->$2)::int, (power_levels->'$3')::int, $4)
	// 		FROM mx_room_state WHERE room_id=$1`, roomID, eventType.Type, defaultType, defaultValue)
	// 	if row == nil {
	// 		// Power levels not in db
	// 		return defaultValue
	// 	}
	// 	var powerLevel int
	// 	err := row.Scan(&powerLevel)
	// 	if err != nil {
	// 		store.log.Errorln("Failed to scan power level for %s in %s: %v", eventType, roomID, err)
	// 	}
	// 	return powerLevel
	// }
	return store.GetPowerLevels(roomID).GetEventLevel(eventType)
}

func (store *SQLStateStore) HasPowerLevel(roomID id.RoomID, userID id.UserID, eventType event.Type) bool {
	// if store.db.dialect == "postgres" {
	// 	defaultType := "events_default"
	// 	defaultValue := 0
	// 	if eventType.IsState() {
	// 		defaultType = "state_default"
	// 		defaultValue = 50
	// 	}
	// 	row := store.db.QueryRow(`SELECT
	// 		COALESCE((power_levels->'users'->$2)::int, (power_levels->'users_default')::int, 0)
	// 		>=
	// 		COALESCE((power_levels->'events'->$3)::int, (power_levels->'$4')::int, $5)
	// 		FROM mx_room_state WHERE room_id=$1`, roomID, userID, eventType.Type, defaultType, defaultValue)
	// 	if row == nil {
	// 		// Power levels not in db
	// 		return defaultValue == 0
	// 	}
	// 	var hasPower bool
	// 	err := row.Scan(&hasPower)
	// 	if err != nil {
	// 		store.log.Errorln("Failed to scan power level for %s in %s: %v", eventType, roomID, err)
	// 	}
	// 	return hasPower
	// }
	// return store.GetPowerLevel(roomID, userID) >= store.GetPowerLevelRequirement(roomID, eventType)
	return false
}
