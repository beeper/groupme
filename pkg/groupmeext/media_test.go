package groupmeext

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestMediaRemoteResponses(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		status     int
		size       int64
		wantError  bool
	}{
		{"image bytes", "image", 200, 4, false},
		{"video bytes", "video", 200, 4, false},
		{"denied", "image", 403, 4, true},
		{"server error", "video", 503, 4, true},
		{"oversized", "image", 200, MaxMediaSize + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = prev })
			http.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("wrong download method")
				}
				if tc.kind == "video" {
					cookie, err := r.Cookie("token")
					if r.URL.String() != "https://m.groupme.com/test" || err != nil || cookie.Value != "test-token" {
						t.Fatal("incorrect native video request")
					}
				} else if r.URL.String() != "https://i.groupme.com/test" || r.Header.Get("Cookie") != "" {
					t.Fatal("incorrect native image request")
				}
				return &http.Response{StatusCode: tc.status, ContentLength: tc.size, Header: http.Header{"Content-Type": {tc.kind + "/test"}}, Body: io.NopCloser(strings.NewReader("data"))}, nil
			})
			var data []byte
			var mime string
			var err error
			if tc.kind == "image" {
				data, mime, err = DownloadImage(context.Background(), "https://i.groupme.com/test")
			} else {
				data, mime, err = DownloadVideo(context.Background(), "https://m.groupme.com/test", "test-token")
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if err == nil && (string(data) != "data" || mime != tc.kind+"/test") {
				t.Fatal("lost native media response")
			}
		})
	}
}

func TestFileRemoteResponses(t *testing.T) {
	const metadata = `[{"file_data":{"file_name":"test.pdf","file_size":4,"mime_type":"application/pdf"}}]`
	for _, tc := range []struct {
		name, metadata string
		status         int
		wantError      bool
	}{
		{"file", metadata, 200, false},
		{"bytes denied", metadata, 403, true},
		{"malformed metadata", `{`, 200, true},
		{"missing metadata", `[]`, 200, true},
		{"missing filename", `[{"file_data":{}}]`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = prev })
			requests := 0
			http.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.Method != "POST" || r.URL.Host != "file.groupme.com" || r.Header.Get("X-Access-Token") != "test-token" {
					t.Fatal("incorrect file authentication or method")
				}
				body, status := "data", tc.status
				if requests == 1 {
					requestBody, _ := io.ReadAll(r.Body)
					if r.URL.Path != "/v1/88/fileData" || string(requestBody) != `{"file_ids":["file-1"]}` {
						t.Fatal("incorrect native metadata request")
					}
					body, status = tc.metadata, 200
				} else if r.URL.Path != "/v1/88/files/file-1" {
					t.Fatal("incorrect native file request")
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			data, name, mime, err := DownloadFile(context.Background(), "88", "file-1", "test-token")
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected file error: %v", err)
			}
			if err == nil && (string(data) != "data" || name != "test.pdf" || mime != "application/pdf") {
				t.Fatal("lost native file response")
			}
			wantRequests := 1
			if tc.metadata == metadata {
				wantRequests = 2
			}
			if requests != wantRequests {
				t.Fatal("invalid metadata triggered a byte download")
			}
		})
	}
}

func TestUploadRejectsMalformedResponses(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"foreign status host", `{"status_url":"https://example.com/token-target"}`},
		{"missing status URL", `{}`},
		{"missing completed file ID", `{"status_url":"https://file.groupme.com/status/test"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = prev })
			http.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "file.groupme.com" {
					t.Fatal("credential sent to another host")
				}
				body := tc.body
				if r.Method == http.MethodGet {
					body = `{"status":"completed"}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			if _, err := UploadFile(context.Background(), "7", "test-token", "test.pdf", []byte("pdf"), "application/pdf"); err == nil {
				t.Fatal("accepted malformed upload response")
			}
		})
	}
	underlying := errors.New("connection refused")
	err := mediaError(&url.Error{Op: "Put", URL: "https://cdn2.groupme.com/private?sig=secret", Err: underlying})
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") || !errors.Is(err, underlying) {
		t.Fatal("unsafe media transport error")
	}
}
