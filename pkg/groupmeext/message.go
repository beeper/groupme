package groupmeext

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/beeper/groupme-lib"
)

// DownloadImage fetches an unauthenticated image or avatar from GroupMe.
func DownloadImage(ctx context.Context, imageURL string) ([]byte, string, error) {
	return downloadMediaURL(ctx, imageURL, "")
}

// DownloadVideo uses the native token-cookie authentication contract.
func DownloadVideo(ctx context.Context, videoURL, token string) ([]byte, string, error) {
	return downloadMediaURL(ctx, videoURL, token)
}

func downloadMediaURL(ctx context.Context, mediaURL, token string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("invalid GroupMe media URL")
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "token", Value: token})
	}
	resp, err := mediaRequest(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := readMedia(resp)
	if err != nil {
		return nil, "", err
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return data, mime, nil
}

// FileMetadata describes a group file without downloading its bytes.
type FileMetadata struct {
	FileName string `json:"file_name"`
	FileSize int    `json:"file_size"`
	Mime     string `json:"mime_type"`
}

// DownloadFile resolves a group file's metadata, then fetches its bytes. Both
// native endpoints use X-Access-Token authentication.
func DownloadFile(ctx context.Context, groupID groupme.ID, fileID, token string) (data []byte, filename, mime string, err error) {
	meta, err := GetFileMetadata(ctx, groupID, fileID, token)
	if err != nil {
		return nil, "", "", err
	}
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://file.groupme.com/v1/%s/files/%s", url.PathEscape(string(groupID)), url.PathEscape(fileID)), nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to build file download request")
	}
	dlReq.Header.Set("X-Access-Token", token)

	dlResp, err := mediaRequest(dlReq)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to download file: %w", err)
	}
	defer dlResp.Body.Close()

	data, err = readMedia(dlResp)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to read downloaded file: %w", err)
	}
	return data, meta.FileName, meta.Mime, nil
}

func GetFileMetadata(ctx context.Context, groupID groupme.ID, fileID, token string) (*FileMetadata, error) {
	reqBody, err := json.Marshal(struct {
		FileIDs []string `json:"file_ids"`
	}{FileIDs: []string{fileID}})
	if err != nil {
		return nil, fmt.Errorf("failed to build file metadata request body: %w", err)
	}

	metaReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://file.groupme.com/v1/%s/fileData", url.PathEscape(string(groupID))), bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to build file metadata request")
	}
	metaReq.Header.Set("X-Access-Token", token)
	metaReq.Header.Set("Content-Type", "application/json")

	metaResp, err := mediaRequest(metaReq)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch file metadata: %w", err)
	}
	defer metaResp.Body.Close()

	var meta []struct {
		FileData FileMetadata `json:"file_data"`
	}
	if err := json.NewDecoder(io.LimitReader(metaResp.Body, 1024*1024)).Decode(&meta); err != nil {
		return nil, fmt.Errorf("failed to decode file metadata: %w", err)
	}
	if len(meta) != 1 || meta[0].FileData.FileName == "" || meta[0].FileData.FileSize < 0 || meta[0].FileData.FileSize > MaxMediaSize {
		return nil, fmt.Errorf("GroupMe returned invalid file metadata")
	}
	return &meta[0].FileData, nil
}

// Video sessions require a group ID or recipient ID, plus size and extension.
type createUploadRequest struct {
	FileSize    int64  `json:"FileSize"`
	SenderId    string `json:"SenderId"`
	Extension   string `json:"Extension"`
	GroupId     string `json:"groupId,omitempty"`
	RecipientId string `json:"recipientId,omitempty"`
}

// UploadURL is a temporary signed upload destination. RenderURL and ThumbnailURL
// are attachment URLs; no expiry parameter was observed in those URLs.
type createUploadResponse struct {
	UploadURL    string `json:"uploadUrl"`
	RenderURL    string `json:"renderUrl"`
	ThumbnailURL string `json:"thumbnailUrl"`
}

// UploadVideo creates a session, then PUTs bytes to its signed URL without
// GroupMe credentials. Exactly one of groupID and recipientID must be set.
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

	sessionResp, err := mediaRequest(sessionReq)
	if err != nil {
		return "", "", fmt.Errorf("failed to create upload session: %w", err)
	}
	defer sessionResp.Body.Close()

	var session createUploadResponse
	if err := json.NewDecoder(io.LimitReader(sessionResp.Body, 1024*1024)).Decode(&session); err != nil {
		return "", "", fmt.Errorf("failed to decode upload session response: %w", err)
	}
	if session.UploadURL == "" || session.RenderURL == "" {
		return "", "", fmt.Errorf("GroupMe did not return an upload URL for the video session")
	}

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, session.UploadURL, bytes.NewReader(data))
	if err != nil {
		return "", "", fmt.Errorf("invalid GroupMe video upload URL")
	}
	putReq.Header.Set("Content-Type", mimeType)
	// Required by Azure Blob Storage for a PUT that creates a new blob
	// (confirmed live: omitting this, or a plain PUT with only
	// X-Access-Token and no SAS URL at all, both fail).
	putReq.Header.Set("x-ms-blob-type", "BlockBlob")

	putResp, err := mediaRequest(putReq)
	if err != nil {
		return "", "", fmt.Errorf("failed to upload video bytes: %w", err)
	}
	defer putResp.Body.Close()

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

	createResp, err := mediaRequest(createReq)
	if err != nil {
		return "", fmt.Errorf("failed to start file upload: %w", err)
	}
	defer createResp.Body.Close()

	var created createFileResponse
	if err := json.NewDecoder(io.LimitReader(createResp.Body, 1024*1024)).Decode(&created); err != nil {
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
			return "", fmt.Errorf("invalid GroupMe file status URL")
		}
		if statusReq.URL.Host != "file.groupme.com" {
			return "", fmt.Errorf("invalid GroupMe file status host")
		}
		statusReq.Header.Set("X-Access-Token", token)

		statusResp, err := mediaRequest(statusReq)
		if err != nil {
			return "", fmt.Errorf("failed to check file upload status: %w", err)
		}
		var status fileUploadStatus
		decodeErr := json.NewDecoder(io.LimitReader(statusResp.Body, 1024*1024)).Decode(&status)
		statusResp.Body.Close()
		if decodeErr != nil {
			return "", fmt.Errorf("failed to decode file upload status: %w", decodeErr)
		}

		if status.Status == "completed" {
			if status.FileID == "" {
				return "", fmt.Errorf("GroupMe completed file upload without a file ID")
			}
			return status.FileID, nil
		}
		if status.Status == "failed" {
			return "", fmt.Errorf("GroupMe could not process the uploaded file")
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}

	return "", fmt.Errorf("timed out waiting for GroupMe to finish processing the uploaded file")
}
