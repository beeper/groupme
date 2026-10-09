package groupmeext

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GroupMe documents a 50 MB limit for document sharing.
const MaxMediaSize = 50_000_000

var mediaHTTPClient = &http.Client{
	Timeout: 2 * time.Minute,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many media redirects")
		}
		if err := validateMediaURL(req.URL); err != nil {
			return err
		}
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("X-Access-Token")
			req.Header.Del("Cookie")
		}
		return nil
	},
}

func validateMediaURL(u *url.URL) error {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || !strings.HasSuffix(u.Hostname(), ".groupme.com") {
		return fmt.Errorf("invalid GroupMe media host")
	}
	return nil
}

// ValidateMediaURL checks a native attachment URL before putting it in a media ID.
func ValidateMediaURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid GroupMe media URL")
	}
	return validateMediaURL(u)
}

// Media URLs include bearer upload signatures. Never return a URL-bearing error
// or provider response body to the connector's logger.
func mediaError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("GroupMe media request failed: %w", uerr.Err)
	}
	return err
}

func mediaRequest(req *http.Request) (*http.Response, error) {
	if err := validateMediaURL(req.URL); err != nil {
		return nil, err
	}
	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		return nil, mediaError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("GroupMe media request returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func readMedia(resp *http.Response) ([]byte, error) {
	if resp.ContentLength > MaxMediaSize {
		return nil, fmt.Errorf("GroupMe media exceeds the 50 MB limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxMediaSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxMediaSize {
		return nil, fmt.Errorf("GroupMe media exceeds the 50 MB limit")
	}
	return data, nil
}
