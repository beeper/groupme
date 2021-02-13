package upgrades

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	log "maunium.net/go/maulogger/v2"
)

type Dialect int

const (
	Postgres Dialect = iota
	SQLite
)

func (dialect Dialect) String() string {
	switch dialect {
	case Postgres:
		return "postgres"
	case SQLite:
		return "sqlite3"
	default:
		return ""
	}
}

type upgradeFunc func(*gorm.DB, context) error

type context struct {
	dialect Dialect
	db      *gorm.DB
	log     log.Logger
}

type upgrade struct {
	message string
	fn      upgradeFunc
}

type version struct {
	gorm.Model
	V int
}

const NumberOfUpgrades = 1

var upgrades [NumberOfUpgrades]upgrade

var UnsupportedDatabaseVersion = fmt.Errorf("unsupported database version")

func GetVersion(db *gorm.DB) (int, error) {
	var ver = version{V: 0}
	result := db.FirstOrCreate(&ver, &ver)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) ||
			errors.Is(result.Error, gorm.ErrInvalidField) {
			db.Create(&ver)
			print("create version")

		} else {
			return 0, result.Error
		}
	}
	return int(ver.V), nil
}

func SetVersion(tx *gorm.DB, newVersion int) error {
	err := tx.Where("v IS NOT NULL").Delete(&version{})
	if err.Error != nil {
		return err.Error
	}

	val := version{V: newVersion}
	tx = tx.Create(&val)
	return tx.Error
}

func Run(log log.Logger, dialectName string, db *gorm.DB) error {
	var dialect Dialect
	switch strings.ToLower(dialectName) {
	case "postgres":
		dialect = Postgres
	case "sqlite3":
		dialect = SQLite
	default:
		return fmt.Errorf("unknown dialect %s", dialectName)
	}

	db.AutoMigrate(&version{})
	version, err := GetVersion(db)

	if err != nil {
		return err
	}

	if version > NumberOfUpgrades {
		return UnsupportedDatabaseVersion
	}

	log.Infofln("Database currently on v%d, latest: v%d", version, NumberOfUpgrades)
	for i, upgrade := range upgrades[version:] {
		log.Infofln("Upgrading database to v%d: %s", version+i+1, upgrade.message)
		err = db.Transaction(func(tx *gorm.DB) error {
			err = upgrade.fn(tx, context{dialect, db, log})
			if err != nil {
				return err
			}
			err = SetVersion(tx, version+i+1)
			if err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}

	}
	return nil
}
