package upgrades

import (
	"gorm.io/gorm"
)

func init() {
	upgrades[0] = upgrade{"Initial schema", func(tx *gorm.DB, ctx context) error {
		tx.Exec(`CREATE TABLE IF NOT EXISTS portal (
			jid      VARCHAR(255),
			receiver VARCHAR(255),
			mxid     VARCHAR(255) UNIQUE,

			name   VARCHAR(255) NOT NULL,
			topic  VARCHAR(512) NOT NULL,
			avatar VARCHAR(255) NOT NULL,
			avatar_url VARCHAR(255),
			encrypted BOOLEAN NOT NULL DEFAULT false,

			PRIMARY KEY (jid, receiver)
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS puppet (
			jid          VARCHAR(255) PRIMARY KEY,
			avatar       VARCHAR(255),
			displayname  VARCHAR(255),
			name_quality SMALLINT,
			custom_mxid VARCHAR(255),
			access_token VARCHAR(1023),
			next_batch VARCHAR(255),
			avatar_url VARCHAR(255),
			enable_presence BOOLEAN NOT NULL DEFAULT true,
			enable_receipts BOOLEAN NOT NULL DEFAULT true
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS "user" (
			mxid VARCHAR(255) PRIMARY KEY,
			jid  VARCHAR(255) UNIQUE,

			management_room VARCHAR(255),

			client_id    VARCHAR(255),
			client_token VARCHAR(255),
			server_token VARCHAR(255),
			enc_key      bytea,
			mac_key      bytea,
			last_connection BIGINT NOT NULL DEFAULT 0
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS "user_portal" (
			user_jid        VARCHAR(255),
			portal_jid      VARCHAR(255),
			portal_receiver VARCHAR(255),
			in_community BOOLEAN NOT NULL DEFAULT FALSE,

			PRIMARY KEY (user_jid, portal_jid, portal_receiver),

			FOREIGN KEY (user_jid) REFERENCES "user"(jid) ON DELETE CASCADE,
			FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal(jid, receiver) ON DELETE CASCADE
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS message (
			chat_jid      VARCHAR(255),
			chat_receiver VARCHAR(255),
			jid           VARCHAR(255),
			mxid          VARCHAR(255) NOT NULL UNIQUE,
			sender        VARCHAR(255) NOT NULL,
			content       bytea        NOT NULL,
			timestamp 	  BIGINT       NOT NULL DEFAULT 0,

			PRIMARY KEY (chat_jid, chat_receiver, jid),
			FOREIGN KEY (chat_jid, chat_receiver) REFERENCES portal(jid, receiver) ON DELETE CASCADE
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS mx_registrations (
			user_id VARCHAR(255) PRIMARY KEY
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS mx_room_state (
			room_id      VARCHAR(255) PRIMARY KEY,
			power_levels TEXT
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS mx_user_profile (
			room_id     VARCHAR(255),
			user_id     VARCHAR(255),
			membership  VARCHAR(15) NOT NULL,
			PRIMARY KEY (room_id, user_id),
			displayname TEXT,
			avatar_url VARCHAR(255)
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS crypto_olm_session (
			session_id   CHAR(43)  NOT NULL,
			sender_key   CHAR(43)  NOT NULL,
			session      bytea     NOT NULL,
			created_at   timestamp NOT NULL,
			last_used    timestamp NOT NULL,
			account_id   TEXT      NOT NULL,
			PRIMARY KEY (account_id, session_id)
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS crypto_megolm_inbound_session (
			session_id   CHAR(43)     NOT NULL,
			sender_key   CHAR(43)     NOT NULL,
			signing_key  CHAR(43)     NOT NULL,
			room_id      TEXT         NOT NULL,
			session      bytea        NOT NULL,
			forwarding_chains bytea   NOT NULL,
			account_id   TEXT         NOT NULL,
			withheld_code TEXT,
			withheld_reason TEXT,
			PRIMARY KEY (session_id, account_id)
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS crypto_device (
			user_id      VARCHAR(255),
			device_id    VARCHAR(255),
			identity_key CHAR(43)      NOT NULL,
			signing_key  CHAR(43)      NOT NULL,
			trust        SMALLINT      NOT NULL,
			deleted      BOOLEAN       NOT NULL,
			name         VARCHAR(255)  NOT NULL,

			PRIMARY KEY (user_id, device_id)
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS crypto_tracked_user (
			user_id VARCHAR(255) PRIMARY KEY
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS crypto_message_index (
			sender_key CHAR(43),
			session_id CHAR(43),
			"index"    INTEGER,
			event_id   VARCHAR(255) NOT NULL,
			timestamp  BIGINT       NOT NULL,

			PRIMARY KEY (sender_key, session_id, "index")
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS crypto_account (
			device_id  TEXT         NOT NULL,
			shared     BOOLEAN      NOT NULL,
			sync_token TEXT         NOT NULL,
			account    bytea        NOT NULL,
			account_id TEXT         PRIMARY KEY
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS crypto_megolm_outbound_session (
			room_id       VARCHAR(255) NOT NULL,
			session_id    CHAR(43)     NOT NULL UNIQUE,
			session       bytea        NOT NULL,
			shared        BOOLEAN      NOT NULL,
			max_messages  INTEGER      NOT NULL,
			message_count INTEGER      NOT NULL,
			max_age       BIGINT       NOT NULL,
			created_at    timestamp    NOT NULL,
			last_used     timestamp    NOT NULL,
			account_id    TEXT,
			PRIMARY KEY (room_id, account_id)
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS crypto_cross_signing_keys (
			user_id TEXT         NOT NULL,
			usage   TEXT         NOT NULL,
			key     CHAR(43)     NOT NULL,
			PRIMARY KEY (user_id, usage)
		)`)

		tx.Exec(`CREATE TABLE IF NOT EXISTS crypto_cross_signing_signatures (
			signed_user_id TEXT         NOT NULL,
			signed_key     TEXT         NOT NULL,
			signer_user_id TEXT         NOT NULL,
			signer_key     TEXT         NOT NULL,
			signature      TEXT         NOT NULL,
			PRIMARY KEY (signed_user_id, signed_key, signer_user_id, signer_key)
		)`)

		return nil
	}}
}
