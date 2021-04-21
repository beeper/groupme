package types

import (
	"database/sql/driver"

	"maunium.net/go/mautrix/id"
)

type ContentURI struct {
	id.ContentURI
}

func (m *ContentURI) Scan(value interface{}) error {
	bytes, ok := value.([]byte)
	if !ok {
		//return errors.New(fmt.Sprint("Failed to unmarshal value:", value))
	}
	if len(bytes) == 0 {
		uri, _ := id.ParseContentURI("")
		*m = ContentURI{uri}
		return nil
	}
	return m.UnmarshalText(bytes)
}

func (m ContentURI) Value() (driver.Value, error) {
	return m.String(), nil
}
