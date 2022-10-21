-- v0 -> v1: Latest revision

CREATE TABLE "user" (
	mxid TEXT PRIMARY KEY,
	gmid TEXT UNIQUE,

    auth_token TEXT,

	management_room TEXT,
    space_room      TEXT,
);

CREATE TABLE portal (
    gmid     TEXT,
	receiver TEXT,
	mxid     TEXT UNIQUE,

    name       TEXT    NOT NULL,
    name_set   BOOLEAN NOT NULL DEFAULT false,
    topic      TEXT    NOT NULL,
    topic_set  BOOLEAN NOT NULL DEFAULT false,
    avatar     TEXT    NOT NULL,
    avatar_url TEXT,
    avatar_set BOOLEAN NOT NULL DEFAULT false,
	encrypted  BOOLEAN NOT NULL DEFAULT false,

	PRIMARY KEY (gmid, receiver)
);

CREATE TABLE puppet (
	gmid            TEXT PRIMARY KEY,
	displayname     TEXT,
    name_set        BOOLEAN NOT NULL DEFAULT false,
	avatar          TEXT,
	avatar_url      TEXT,
    avatar_set      BOOLEAN NOT NULL DEFAULT false,

	custom_mxid     TEXT,
	access_token    TEXT,
	next_batch      TEXT,

	enable_presence BOOLEAN NOT NULL DEFAULT true,
	enable_receipts BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE message (
	chat_gmid     TEXT,
	chat_receiver TEXT,
	gmid          TEXT,
	mxid          TEXT UNIQUE,
	sender        TEXT,
	timestamp     BIGINT,
    sent          BOOLEAN,

	PRIMARY KEY (chat_gmid, chat_receiver, gmid),
	FOREIGN KEY (chat_gmid, chat_receiver) REFERENCES portal(gmid, receiver) ON DELETE CASCADE
);

CREATE TABLE reaction (
    chat_gmid     TEXT,
    chat_receiver TEXT,
    target_gmid   TEXT,
    sender        TEXT,

    mxid TEXT NOT NULL,
    gmid TEXT NOT NULL,

    PRIMARY KEY (chat_gmid, chat_receiver, target_gmid, sender),
    FOREIGN KEY (chat_gmid, chat_receiver, target_gmid) REFERENCES message(chat_gmid, chat_receiver, gmid)
        ON DELETE CASCADE ON UPDATE CASCADE
)

CREATE TABLE user_portal (
	user_mxid       TEXT,
	portal_gmid     TEXT,
	portal_receiver TEXT,
	in_space        BOOLEAN NOT NULL DEFAULT false,

	PRIMARY KEY (user_mxid, portal_gmid, portal_receiver),

	FOREIGN KEY (user_mxid)                    REFERENCES "user"(mxid)           ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY (portal_gmid, portal_receiver) REFERENCES portal(gmid, receiver) ON UPDATE CASCADE ON DELETE CASCADE
);
