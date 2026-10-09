// Package groupme defines a client capable of executing API commands for the GroupMe chat service
package groupme

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// GroupMe documentation: https://dev.groupme.com/docs/image_service
//
// This is a local addition -- not present in the pinned upstream version of
// this library at all (it only ever implemented the message/group/like/etc.
// CRUD endpoints, never the separate image service host). Needed to support
// outgoing image attachments (Matrix -> GroupMe), which the bridge never
// supported even before this library was pinned. See
// pkg/connector/handlematrix.go for the caller.

// imageServiceUploadURL is on a different host (image.groupme.com) than the
// rest of the v3 API (api.groupme.com) -- not built from c.endpointBase.
const imageServiceUploadURL = "https://image.groupme.com/pictures"

// imageUploadResponse mirrors the documented response shape:
//
//	{"payload": {"url": "https://i.groupme.com/...", "picture_url": "https://i.groupme.com/..."}}
//
// url and picture_url are documented as identical; url is used since it's
// listed first and matches the field name used when attaching an image to
// an outgoing message (Attachment.URL).
type imageUploadResponse struct {
	Payload struct {
		URL string `json:"url"`
	} `json:"payload"`
}

// UploadImage uploads raw image bytes to GroupMe's image service and
// returns the resulting i.groupme.com URL, for use in an outgoing
// Message's Attachments (Attachment{Type: Image, URL: <this>}). Unlike the
// rest of this package's endpoints, auth here is an X-Access-Token header,
// not a `token` query parameter (confirmed against the docs cited above).
func (c *Client) UploadImage(ctx context.Context, data []byte, contentType string) (string, error) {
	if contentType == "" {
		contentType = "image/jpeg"
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", imageServiceUploadURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("X-Access-Token", c.authorizationToken)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("image upload request failed: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read image upload response: %w", err)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return "", fmt.Errorf("image upload failed with status %d", httpResp.StatusCode)
	}

	var resp imageUploadResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("failed to parse image upload response: %w", err)
	}
	if resp.Payload.URL == "" {
		return "", fmt.Errorf("image upload response had no payload.url")
	}
	return resp.Payload.URL, nil
}
