module github.com/karmanyaahm/matrix-groupme-go

go 1.15

require (
	github.com/Rhymen/go-whatsapp v0.1.1
	github.com/gabriel-vasile/mimetype v1.1.2
	github.com/gorilla/websocket v1.4.2
	github.com/jackc/pgproto3/v2 v2.0.7 // indirect
	github.com/karmanyaahm/groupme v0.0.0
	github.com/karmanyaahm/wray v0.0.0-20210303233435-756d58657c14 // indirect
	github.com/lib/pq v1.9.0
	github.com/mattn/go-sqlite3 v1.14.6
	github.com/prometheus/client_golang v1.9.0
	github.com/prometheus/procfs v0.6.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
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

replace github.com/karmanyaahm/groupme => ./groupme
