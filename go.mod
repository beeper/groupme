module github.com/karmanyaahm/matrix-groupme-go

go 1.15

require (
	github.com/Rhymen/go-whatsapp v0.1.1
	github.com/gabriel-vasile/mimetype v1.1.2
	github.com/gorilla/websocket v1.4.2
	github.com/jackc/pgproto3/v2 v2.0.7 // indirect
	github.com/karmanyaahm/groupme v0.0.0
	github.com/karmanyaahm/wray v0.0.0-20210303233435-756d58657c14
	github.com/lib/pq v1.9.0
	github.com/mattn/go-sqlite3 v1.14.6
	github.com/prometheus/client_golang v1.9.0
	github.com/prometheus/procfs v0.6.0 // indirect
	golang.org/x/text v0.3.5 // indirect
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	google.golang.org/protobuf v1.25.0 // indirect
	gopkg.in/yaml.v2 v2.4.0
	gorm.io/driver/postgres v1.0.8
	gorm.io/driver/sqlite v1.1.4
	gorm.io/gorm v1.20.12
	maunium.net/go/mauflag v1.0.0
	maunium.net/go/maulogger/v2 v2.2.4
	maunium.net/go/mautrix v0.9.24
)

replace github.com/karmanyaahm/groupme => ../groupme-lib
