package database

import (
	"database/sql"
	"errors"
)

func (user *User) IsInSpace(portal PortalKey) bool {
	user.inSpaceCacheLock.Lock()
	defer user.inSpaceCacheLock.Unlock()
	if cached, ok := user.inSpaceCache[portal]; ok {
		return cached
	}
	var inSpace bool
	err := user.db.QueryRow("SELECT in_space FROM user_portal WHERE user_mxid=$1 AND portal_gmid=$2 AND portal_receiver=$3", user.MXID, portal.GMID, portal.Receiver).Scan(&inSpace)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		user.log.Warnfln("Failed to scan in space status from user portal table: %v", err)
	}
	user.inSpaceCache[portal] = inSpace
	return inSpace
}

func (user *User) MarkInSpace(portal PortalKey) {
	user.inSpaceCacheLock.Lock()
	defer user.inSpaceCacheLock.Unlock()
	_, err := user.db.Exec(`
			INSERT INTO user_portal (user_mxid, portal_gmid, portal_receiver, in_space) VALUES ($1, $2, $3, true)
			ON CONFLICT (user_mxid, portal_gmid, portal_receiver) DO UPDATE SET in_space=true
		`, user.MXID, portal.GMID, portal.Receiver)
	if err != nil {
		user.log.Warnfln("Failed to update in space status: %v", err)
	} else {
		user.inSpaceCache[portal] = true
	}
}
