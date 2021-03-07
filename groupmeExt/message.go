package groupmeExt

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"

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

//DownloadImage helper function to download image from groupme;
// append .large/.preview/.avatar to get various sizes
func DownloadImage(URL string) (bytes *[]byte, mime string, err error) {
	//TODO check its actually groupme?
	response, err := http.Get(URL)
	if err != nil {
		return nil, "", errors.New("Failed to download avatar: " + err.Error())
	}
	defer response.Body.Close()

	image, err := ioutil.ReadAll(response.Body)
	bytes = &image
	if err != nil {
		return nil, "", errors.New("Failed to read downloaded image:" + err.Error())
	}

	mime = response.Header.Get("Content-Type")
	if len(mime) == 0 {
		mime = http.DetectContentType(image)
	}
	return
}
