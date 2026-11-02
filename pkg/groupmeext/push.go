package groupmeext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var pushHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// GroupMe Android 16.25.9: PushRegistrationHelper.buildJsonBody and
// registerTokenWithGroupMe use the v2 host and the legacy GCM service name.
func RegisterFCMPush(ctx context.Context, accessToken, pushToken string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(accessToken) == "" || strings.TrimSpace(pushToken) == "" {
		return fmt.Errorf("GroupMe access token and FCM token are required")
	}
	type registration struct {
		Service        string `json:"service"`
		RegistrationID string `json:"registration_id"`
	}
	want := registration{Service: "google_cloud_messaging", RegistrationID: pushToken}
	body, err := json.Marshal(struct {
		Registration registration `json:"push_registration"`
	}{want})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://v2.groupme.com/push_registrations", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Access-Token", accessToken)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp, err := pushHTTPClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		for errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("GroupMe push registration failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("GroupMe push registration returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Response struct {
			Registration *registration `json:"push_registration"`
		} `json:"response"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result); err != nil {
		return fmt.Errorf("invalid GroupMe push registration response")
	}
	if result.Response.Registration == nil || *result.Response.Registration != want {
		return fmt.Errorf("GroupMe push registration response does not match the requested registration")
	}
	return nil
}
