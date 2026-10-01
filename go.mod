module github.com/beeper/groupme

go 1.26.0

toolchain go1.27.1

require (
	github.com/beeper/groupme-lib v0.2.1-0.20221021205945-8f23e04eea71
	github.com/coder/websocket v1.8.15
	github.com/google/uuid v1.2.0
	github.com/karmanyaahm/wray v0.0.0-20210303233435-756d58657c14
	github.com/rs/zerolog v1.35.1
	go.mau.fi/util v0.10.1
	maunium.net/go/mautrix v0.31.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/lib/pq v1.12.3 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-sqlite3 v1.14.52 // indirect
	github.com/petermattis/goid v0.0.0-20260820044319-269ab09b5261 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/yuin/goldmark v1.8.6 // indirect
	go.mau.fi/zeroconfig v0.2.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

// Local patch to force HTTP/1.1 for GroupMe's Faye/Bayeux push transport;
// see thirdparty/wray/README.md.
replace github.com/karmanyaahm/wray => ./thirdparty/wray

// Local patch adding Message.Reactions (GroupMe's newer per-emoji reaction
// data; not present in this library as pinned in 2022) and the Reaction
// type. See thirdparty/groupme-lib/json.go.
replace github.com/beeper/groupme-lib => ./thirdparty/groupme-lib
