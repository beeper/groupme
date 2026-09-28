package groupmeext

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/beeper/groupme-lib"
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

// DownloadImage downloads an image attachment from GroupMe's image CDN
// (i.groupme.com), a plain unauthenticated GET.
func DownloadImage(url string) (data *[]byte, mime string, err error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()
	// Without this, an error response was uploaded to Matrix as if it were
	// the image: 2,825 S3 "AccessDenied" XML pages (from expired/removed
	// GroupMe image URLs, mostly old avatars) ended up in the media store
	// on 2026-09-18..21, showing as broken pictures.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("failed to download image: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read downloaded image: %w", err)
	}

	mime = resp.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(body)
	}
	return &body, mime, nil
}

// DownloadVideo downloads a video attachment. Unlike images (a plain
// public GET) and files (a signed X-Access-Token API call, see
// DownloadFile), GroupMe's video CDN authenticates via a "token" cookie
// carrying the account's access token -- ported as-is from the pre-2023
// bridge (the only place this was ever verified to work against the real
// API); not separately re-verified live in this revival, see NOTES.md.
func DownloadVideo(videoURL, token string) (data []byte, mime string, err error) {
	req, err := http.NewRequest(http.MethodGet, videoURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build video request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "token", Value: token})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download video: %w", err)
	}
	defer resp.Body.Close()

	data, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read downloaded video: %w", err)
	}

	mime = resp.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return data, mime, nil
}

// fileMetadata is the response shape of file.groupme.com's "fileData"
// lookup endpoint (see DownloadFile).
type fileMetadata struct {
	FileData struct {
		FileName string `json:"file_name"`
		FileSize int    `json:"file_size"`
		Mime     string `json:"mime_type"`
	} `json:"file_data"`
}

// DownloadFile downloads a "file" attachment (GroupMe's group file-sharing
// feature, distinct from image/video message attachments) via
// file.groupme.com. This is a two-step API, both authenticated via
// X-Access-Token: one call resolves the file's name/mime type, a second
// fetches its bytes. groupID is the containing group's ID -- GroupMe's
// file-sharing feature is group-only, so this isn't expected to be called
// for a DM attachment.
//
// Ported from the pre-2023 bridge's equivalent, which used to panic() on
// any request error here -- a network hiccup on a single file attachment
// would have taken down the entire bridge process. Rewritten to return an
// error instead, same as every other attachment download path.
func DownloadFile(groupID groupme.ID, fileID, token string) (data []byte, filename, mime string, err error) {
	reqBody, err := json.Marshal(struct {
		FileIDs []string `json:"file_ids"`
	}{FileIDs: []string{fileID}})
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to build file metadata request body: %w", err)
	}

	metaReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://file.groupme.com/v1/%s/fileData", groupID), bytes.NewReader(reqBody))
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to build file metadata request: %w", err)
	}
	metaReq.Header.Set("X-Access-Token", token)
	metaReq.Header.Set("Content-Type", "application/json")

	metaResp, err := http.DefaultClient.Do(metaReq)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to fetch file metadata: %w", err)
	}
	defer metaResp.Body.Close()

	var meta []fileMetadata
	if err := json.NewDecoder(metaResp.Body).Decode(&meta); err != nil {
		return nil, "", "", fmt.Errorf("failed to decode file metadata: %w", err)
	}
	if len(meta) == 0 {
		return nil, "", "", fmt.Errorf("GroupMe returned no metadata for file %s", fileID)
	}

	dlReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://file.groupme.com/v1/%s/files/%s", groupID, fileID), nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to build file download request: %w", err)
	}
	dlReq.Header.Set("X-Access-Token", token)

	dlResp, err := http.DefaultClient.Do(dlReq)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to download file: %w", err)
	}
	defer dlResp.Body.Close()

	data, err = io.ReadAll(dlResp.Body)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to read downloaded file: %w", err)
	}

	return data, meta[0].FileData.FileName, meta[0].FileData.Mime, nil
}

// createUploadRequest is the body of the m.groupme.com/uploads call that
// begins an outgoing video upload (see UploadVideo). Reverse-engineered
// live: dev.groupme.com's docs don't cover outgoing video at all, and
// there was no existing implementation (incoming or outgoing) to work
// from. The exact field set and required-ness was determined by reading
// the endpoint's own ASP.NET model-validation error messages (it replies
// with e.g. {"errors":{"FileSize":["The FileSize field is required."]}}
// for a missing/wrong field) rather than guessing blind -- GroupId and
// RecipientId aren't both required, but at least one is ("You must
// provide either recipientId or groupId").
type createUploadRequest struct {
	FileSize    int64  `json:"FileSize"`
	SenderId    string `json:"SenderId"`
	Extension   string `json:"Extension"`
	GroupId     string `json:"groupId,omitempty"`
	RecipientId string `json:"recipientId,omitempty"`
}

// createUploadResponse is m.groupme.com/uploads' response: a short-lived
// SAS-signed Azure Blob Storage URL to PUT the actual video bytes to, plus
// the public (unsigned, permanent) URLs to reference once that PUT
// completes. TranscriptURL is unused here (presumably for closed
// captions/transcription, unconfirmed) and always observed null live.
type createUploadResponse struct {
	UploadURL     string `json:"uploadUrl"`
	RenderURL     string `json:"renderUrl"`
	ThumbnailURL  string `json:"thumbnailUrl"`
	TranscriptURL string `json:"transcriptUrl"`
}

// UploadVideo uploads an outgoing video to GroupMe, returning the URLs to
// use as a "video" attachment's url/preview_url (see convertGroupMeMessage
// in pkg/connector for the incoming equivalent, and NOTES.md "Outgoing
// video/file attachments" for how this was reverse-engineered).
//
// Two real HTTP calls, not one: first m.groupme.com/uploads is asked to
// create an upload session for a video of this size/extension belonging
// to this group or DM conversation, which hands back a one-time SAS URL
// scoped to Azure Blob Storage (cdn2.groupme.com) -- GroupMe's own
// X-Access-Token auth doesn't apply to that second request at all, unlike
// every other call in this file; the SAS signature in the URL itself is
// the only auth Azure wants, and a real attempt to PUT directly to
// cdn2.groupme.com with just X-Access-Token (no SAS) was confirmed live
// to fail with Azure's own "PublicAccessNotPermitted" error.
//
// groupID and recipientID are mutually exclusive -- exactly one should be
// non-empty, matching whether this is a group or DM send (see the "You
// must provide either recipientId or groupId" validation error mentioned
// on createUploadRequest).
func UploadVideo(ctx context.Context, token, senderID, groupID, recipientID string, data []byte, extension, mimeType string) (renderURL, thumbnailURL string, err error) {
	reqBody, err := json.Marshal(createUploadRequest{
		FileSize:    int64(len(data)),
		SenderId:    senderID,
		Extension:   extension,
		GroupId:     groupID,
		RecipientId: recipientID,
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to build upload session request: %w", err)
	}

	sessionReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://m.groupme.com/uploads", bytes.NewReader(reqBody))
	if err != nil {
		return "", "", fmt.Errorf("failed to build upload session request: %w", err)
	}
	sessionReq.Header.Set("X-Access-Token", token)
	sessionReq.Header.Set("Content-Type", "application/json")

	sessionResp, err := http.DefaultClient.Do(sessionReq)
	if err != nil {
		return "", "", fmt.Errorf("failed to create upload session: %w", err)
	}
	defer sessionResp.Body.Close()

	var session createUploadResponse
	if err := json.NewDecoder(sessionResp.Body).Decode(&session); err != nil {
		return "", "", fmt.Errorf("failed to decode upload session response: %w", err)
	}
	if session.UploadURL == "" {
		return "", "", fmt.Errorf("GroupMe did not return an upload URL for the video session")
	}

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, session.UploadURL, bytes.NewReader(data))
	if err != nil {
		return "", "", fmt.Errorf("failed to build video upload request: %w", err)
	}
	putReq.Header.Set("Content-Type", mimeType)
	// Required by Azure Blob Storage for a PUT that creates a new blob
	// (confirmed live: omitting this, or a plain PUT with only
	// X-Access-Token and no SAS URL at all, both fail).
	putReq.Header.Set("x-ms-blob-type", "BlockBlob")

	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return "", "", fmt.Errorf("failed to upload video bytes: %w", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode >= 300 {
		body, _ := io.ReadAll(putResp.Body)
		return "", "", fmt.Errorf("uploading video bytes failed with status %d: %s", putResp.StatusCode, body)
	}

	return session.RenderURL, session.ThumbnailURL, nil
}

// createFileResponse is file.groupme.com's response to starting a file
// upload (see UploadFile): the upload is processed asynchronously, so
// this just points to where to poll for completion.
type createFileResponse struct {
	StatusURL string `json:"status_url"`
}

// fileUploadStatus is the response shape of the status_url a file upload
// returns (see UploadFile). Status is "completed" once FileID is ready to
// use; other values (e.g. some in-progress state) weren't observed live
// since a small test file completed within the first poll every time this
// was tried, so the exact set of possible Status values beyond
// "completed" is unconfirmed.
type fileUploadStatus struct {
	Status string `json:"status"`
	FileID string `json:"file_id"`
}

// UploadFile uploads an outgoing file (GroupMe's group file-sharing
// feature -- see DownloadFile's doc comment) and returns its file_id, for
// use as a "file" attachment's file_id. Reverse-engineered live the same
// way as UploadVideo; see NOTES.md "Outgoing video/file attachments".
//
// Unlike video, this doesn't need a separate SAS-signed upload step --
// file.groupme.com is GroupMe's own file-service (confirmed via its
// response headers, e.g. "x-gm-service: file-service"), not a direct
// Azure Blob Storage passthrough, so a plain X-Access-Token-authenticated
// POST of the raw bytes is enough on its own.
//
// filename is passed as a "name" query parameter -- confirmed live to be
// the *only* thing that makes DownloadFile's metadata lookup come back
// populated afterward: neither a multipart form body (with a proper
// Content-Disposition filename -- the content didn't even transfer that
// way, this endpoint appears to not parse multipart at all), nor the
// Content-Type header, nor several other header/query-param names tried,
// had any effect. mime_type is then derived by GroupMe itself from
// filename's extension (confirmed live: a ".pdf" name produced
// "application/pdf" with no mime type passed anywhere else) -- so the
// mimeType parameter here is honored only as this function's own
// Content-Type request header (harmless either way, but not what
// actually determines the stored mime_type); pass a real filename with
// its real extension to get useful metadata out the other end.
func UploadFile(ctx context.Context, groupID groupme.ID, token, filename string, data []byte, mimeType string) (fileID string, err error) {
	uploadURL := fmt.Sprintf("https://file.groupme.com/v1/%s/files", groupID)
	if filename != "" {
		uploadURL += "?name=" + url.QueryEscape(filename)
	}
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to build file upload request: %w", err)
	}
	createReq.Header.Set("X-Access-Token", token)
	if mimeType != "" {
		createReq.Header.Set("Content-Type", mimeType)
	}

	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		return "", fmt.Errorf("failed to start file upload: %w", err)
	}
	defer createResp.Body.Close()

	var created createFileResponse
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		return "", fmt.Errorf("failed to decode file upload response: %w", err)
	}
	if created.StatusURL == "" {
		return "", fmt.Errorf("GroupMe did not return a status URL for the file upload")
	}

	// The upload is processed asynchronously; poll until it reports
	// completed. Every real upload tried during development (all well
	// under 1MB) completed by the very first poll, but this retries with
	// a short fixed delay for a while regardless, in case a larger real
	// file takes longer -- unconfirmed live, since only small test files
	// were ever tried (see this function's doc comment).
	const pollInterval = 500 * time.Millisecond
	const maxAttempts = 20 // ~10s total
	for attempt := 0; attempt < maxAttempts; attempt++ {
		statusReq, err := http.NewRequestWithContext(ctx, http.MethodGet, created.StatusURL, nil)
		if err != nil {
			return "", fmt.Errorf("failed to build file upload status request: %w", err)
		}
		statusReq.Header.Set("X-Access-Token", token)

		statusResp, err := http.DefaultClient.Do(statusReq)
		if err != nil {
			return "", fmt.Errorf("failed to check file upload status: %w", err)
		}
		var status fileUploadStatus
		decodeErr := json.NewDecoder(statusResp.Body).Decode(&status)
		statusResp.Body.Close()
		if decodeErr != nil {
			return "", fmt.Errorf("failed to decode file upload status: %w", decodeErr)
		}

		if status.Status == "completed" && status.FileID != "" {
			return status.FileID, nil
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}

	return "", fmt.Errorf("timed out waiting for GroupMe to finish processing the uploaded file")
}
