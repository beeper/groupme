package connector

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/beeper/groupme-lib"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

//go:embed poll-upgrades/*.sql
var pollUpgrades embed.FS

var pollUpgradeTable = dbutil.BuildUpgradeTable().WithFSPath(pollUpgrades, "poll-upgrades").Finish()

type pollStore struct {
	*dbutil.Database
	bridgeID networkid.BridgeID
}

type pollState struct {
	loginID  networkid.UserLoginID
	portalID networkid.PortalID
	anchorID groupme.ID
	anchorTS groupme.Timestamp
	head     groupme.ID
	headTS   groupme.Timestamp
	before   groupme.ID
	scanned  bool
}

func newPollStore(db *dbutil.Database, bridgeID networkid.BridgeID, log zerolog.Logger) *pollStore {
	return &pollStore{Database: db.Child("groupme_poll_version", pollUpgradeTable, dbutil.ZeroLogger(log)), bridgeID: bridgeID}
}

func (db *pollStore) load(ctx context.Context, loginID networkid.UserLoginID, portalID networkid.PortalID, anchorID groupme.ID, anchorTS groupme.Timestamp) (*pollState, error) {
	_, err := db.Exec(ctx, `INSERT INTO groupme_poll_state (bridge_id, login_id, portal_id, anchor_id, anchor_ts)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, db.bridgeID, loginID, portalID, anchorID, int64(anchorTS))
	if err != nil {
		return nil, err
	}
	s := &pollState{loginID: loginID, portalID: portalID}
	err = db.QueryRow(ctx, `SELECT anchor_id, anchor_ts, scan_head, scan_head_ts, scan_before, scanned
		FROM groupme_poll_state WHERE bridge_id=$1 AND login_id=$2 AND portal_id=$3`, db.bridgeID, loginID, portalID).
		Scan(&s.anchorID, &s.anchorTS, &s.head, &s.headTS, &s.before, &s.scanned)
	return s, err
}

func (db *pollStore) stage(ctx context.Context, s *pollState, messages []*groupme.Message) error {
	return db.DoTxn(ctx, nil, func(ctx context.Context) error {
		for _, msg := range messages {
			payload, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			_, err = db.Exec(ctx, `INSERT INTO groupme_poll_message (bridge_id, login_id, portal_id, message_id, message_ts, payload)
				VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, db.bridgeID, s.loginID, s.portalID, msg.ID, int64(msg.CreatedAt), string(payload))
			if err != nil {
				return err
			}
		}
		_, err := db.Exec(ctx, `UPDATE groupme_poll_state SET scan_head=$4, scan_head_ts=$5, scan_before=$6, scanned=$7
			WHERE bridge_id=$1 AND login_id=$2 AND portal_id=$3`, db.bridgeID, s.loginID, s.portalID, s.head, int64(s.headTS), s.before, s.scanned)
		return err
	})
}

func (db *pollStore) pending(ctx context.Context, s *pollState, limit int) ([]*groupme.Message, error) {
	rows, err := db.Query(ctx, `SELECT payload FROM groupme_poll_message WHERE bridge_id=$1 AND login_id=$2 AND portal_id=$3
		ORDER BY message_ts, LENGTH(message_id), message_id LIMIT $4`, db.bridgeID, s.loginID, s.portalID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []*groupme.Message
	for rows.Next() {
		var payload string
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		var msg groupme.Message
		if err = json.Unmarshal([]byte(payload), &msg); err != nil {
			return nil, fmt.Errorf("decode pending poll message: %w", err)
		}
		messages = append(messages, &msg)
	}
	return messages, rows.Err()
}

func (db *pollStore) acknowledge(ctx context.Context, s *pollState, messageID groupme.ID) error {
	_, err := db.Exec(ctx, `DELETE FROM groupme_poll_message WHERE bridge_id=$1 AND login_id=$2 AND portal_id=$3 AND message_id=$4`, db.bridgeID, s.loginID, s.portalID, messageID)
	return err
}

func (db *pollStore) finish(ctx context.Context, s *pollState) error {
	_, err := db.Exec(ctx, `UPDATE groupme_poll_state SET anchor_id=scan_head, anchor_ts=scan_head_ts,
		scan_head='', scan_head_ts=0, scan_before='', scanned=false
		WHERE bridge_id=$1 AND login_id=$2 AND portal_id=$3 AND scanned=true AND NOT EXISTS (
			SELECT 1 FROM groupme_poll_message WHERE bridge_id=$1 AND login_id=$2 AND portal_id=$3
		)`, db.bridgeID, s.loginID, s.portalID)
	return err
}
