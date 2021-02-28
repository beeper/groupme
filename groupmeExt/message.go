package groupmeExt

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/karmanyaahm/groupme"
)

type Message struct{ groupme.Message }

func (m *Message) Scan(value interface{}) error {
	bytes, ok := value.(string)
	if !ok {
		return errors.New(fmt.Sprint("Failed to unmarshal json value:", value))
	}

	message := Message{}
	err := json.Unmarshal([]byte(bytes), &message)

	*m = Message(message)
	return err
}

func (m *Message) Value() (driver.Value, error) {
	e, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return e, nil
}
