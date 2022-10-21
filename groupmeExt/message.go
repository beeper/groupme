package groupmeExt

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/karmanyaahm/groupme"

	"github.com/beeper/groupme/types"
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

// DownloadImage helper function to download image from groupme;
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

func DownloadFile(RoomJID types.GroupMeID, FileID string, token string) (contents []byte, fname, mime string) {
	client := &http.Client{}
	b, _ := json.Marshal(struct {
		FileIDS []string `json:"file_ids"`
	}{
		FileIDS: []string{FileID},
	})

	req, _ := http.NewRequest("POST", fmt.Sprintf("https://file.groupme.com/v1/%s/fileData", RoomJID), bytes.NewReader(b))
	req.Header.Add("X-Access-Token", token)
	req.Header.Add("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// TODO: FIX
		panic(err)
	}

	defer resp.Body.Close()
	data := []ImgData{}
	json.NewDecoder(resp.Body).Decode(&data)
	fmt.Println(data, RoomJID, FileID, token)
	if len(data) < 1 {
		return
	}

	req, _ = http.NewRequest("POST", fmt.Sprintf("https://file.groupme.com/v1/%s/files/%s", RoomJID, FileID), nil)
	req.URL.Query().Add("token", token)
	req.Header.Add("X-Access-Token", token)
	resp, err = client.Do(req)
	if err != nil {
		// TODO: FIX
		panic(err)
	}
	defer resp.Body.Close()

	bytes, _ := ioutil.ReadAll(resp.Body)
	return bytes, data[0].FileData.FileName, data[0].FileData.Mime

}

func DownloadVideo(previewURL, videoURL, token string) (vidContents []byte, mime string) {
	//preview TODO
	client := &http.Client{}

	req, _ := http.NewRequest("GET", videoURL, nil)
	req.AddCookie(&http.Cookie{Name: "token", Value: token})
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return nil, ""
	}
	defer resp.Body.Close()

	bytes, _ := ioutil.ReadAll(resp.Body)
	mime = resp.Header.Get("Content-Type")
	if len(mime) == 0 {
		mime = http.DetectContentType(bytes)
	}
	return bytes, mime

}

type ImgData struct {
	FileData struct {
		FileName string `json:"file_name"`
		FileSize int    `json:"file_size"`
		Mime     string `json:"mime_type"`
	} `json:"file_data"`
}
