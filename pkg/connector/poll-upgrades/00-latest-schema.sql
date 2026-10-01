-- v0 -> v1: Durable polling recovery
CREATE TABLE groupme_poll_state (
    bridge_id TEXT NOT NULL,
    login_id TEXT NOT NULL,
    portal_id TEXT NOT NULL,
    anchor_id TEXT NOT NULL,
    anchor_ts BIGINT NOT NULL,
    scan_head TEXT NOT NULL DEFAULT '',
    scan_head_ts BIGINT NOT NULL DEFAULT 0,
    scan_before TEXT NOT NULL DEFAULT '',
    scanned BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (bridge_id, login_id, portal_id),
    FOREIGN KEY (bridge_id, login_id) REFERENCES user_login (bridge_id, id) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE groupme_poll_message (
    bridge_id TEXT NOT NULL,
    login_id TEXT NOT NULL,
    portal_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    message_ts BIGINT NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY (bridge_id, login_id, portal_id, message_id),
    FOREIGN KEY (bridge_id, login_id, portal_id) REFERENCES groupme_poll_state (bridge_id, login_id, portal_id) ON DELETE CASCADE ON UPDATE CASCADE
);
