package groupmeext

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// GroupMe documents a 50 MB limit for document sharing.
const MaxMediaSize = 50_000_000

var mediaHTTPClient = newMediaHTTPClient(validateMediaURL)
var imageHTTPClient = newMediaHTTPClient(validateImageURL)

func newMediaHTTPClient(validate func(*url.URL) error) *http.Client {
	return &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many media redirects")
			}
			if err := validate(req.URL); err != nil {
				return err
			}
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del("X-Access-Token")
				req.Header.Del("Cookie")
			}
			return nil
		},
	}
}

var giphyImageHost = regexp.MustCompile(`^(media[0-9]*|i)\.giphy\.com$`)

func IsGiphyImageURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" &&
		giphyImageHost.MatchString(u.Hostname()) && strings.HasSuffix(u.Path, ".gif")
}

func validateImageURL(u *url.URL) error {
	if IsGiphyImageURL(u.String()) {
		return nil
	}
	return validateMediaURL(u)
}

func ValidateImageURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid GroupMe image URL")
	}
	return validateImageURL(u)
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

// HTTPError is a non-2xx response from a GroupMe endpoint that groupme-lib
// doesn't cover, so callers can tell rate limits from rejections and outages.
type HTTPError struct {
	Request    string
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", e.Request, e.StatusCode)
}

func mediaRequest(req *http.Request) (*http.Response, error) {
	return requestMedia(req, mediaHTTPClient, validateMediaURL)
}

func requestMedia(req *http.Request, client *http.Client, validate func(*url.URL) error) (*http.Response, error) {
	if err := validate(req.URL); err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, mediaError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, &HTTPError{Request: "GroupMe media request", StatusCode: resp.StatusCode}
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
