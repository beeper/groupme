module maunium.net/go/mautrix-whatsapp

go 1.15

require (
	github.com/Rhymen/go-whatsapp v0.1.1
	github.com/gorilla/websocket v1.4.2
	github.com/jackc/pgproto3/v2 v2.0.7 // indirect
	github.com/karmanyaahm/groupme v0.2.0
	github.com/karmanyaahm/wray v0.0.0-20210129044305-8ca7d2cc2388 // indirect
	github.com/lib/pq v1.9.0
	github.com/mattn/go-sqlite3 v1.14.6
	github.com/prometheus/client_golang v1.9.0
	github.com/prometheus/procfs v0.6.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/tidwall/sjson v1.1.5 // indirect
	golang.org/x/crypto v0.0.0-20201221181555-eec23a3978ad // indirect
	golang.org/x/net v0.0.0-20210119194325-5f4716e94777 // indirect
	golang.org/x/text v0.3.5 // indirect
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	google.golang.org/protobuf v1.25.0 // indirect
	gopkg.in/yaml.v2 v2.4.0
	gorm.io/driver/postgres v1.0.8
	gorm.io/driver/sqlite v1.1.4
	gorm.io/gorm v1.20.12
	maunium.net/go/mauflag v1.0.0
	maunium.net/go/maulogger/v2 v2.1.1
	maunium.net/go/mautrix v0.8.2
)

replace github.com/Rhymen/go-whatsapp => github.com/tulir/go-whatsapp v0.3.16

replace github.com/karmanyaahm/groupme => ../groupme

replace maunium.net/go/mautrix => ../mautrix

replace maunium.net/go/mautrix/i => ../mautrix_go/id
