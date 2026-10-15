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

func TestFCMPushRegistration(t *testing.T) {
	previous := pushHTTPClient
	t.Cleanup(func() { pushHTTPClient = previous })
	const response = `{"response":{"push_registration":{"id":"123","service":"google_cloud_messaging","registration_id":"test-fcm-token","badge_count":0,"created_at":123,"updated_at":456}}}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"created", 201, response, true},
		{"already registered", 200, response, true},
		{"unauthorized", 401, `{"error":"test-access-token"}`, false},
		{"rejected token", 400, `{"error":"test-fcm-token"}`, false},
		{"rate limited", 429, "", false},
		{"server failure", 503, "", false},
		{"empty success", 204, "", false},
		{"invalid JSON", 201, "test-fcm-token", false},
		{"missing registration", 201, `{"response":{}}`, false},
		{"null registration", 201, `{"response":{"push_registration":null}}`, false},
		{"different token", 201, strings.ReplaceAll(response, "test-fcm-token", "other-token"), false},
		{"different service", 201, strings.ReplaceAll(response, "google_cloud_messaging", "apns"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			pushHTTPClient = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.String() != "https://v2.groupme.com/push_registrations" {
					t.Fatalf("unexpected destination: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("X-Access-Token") != "test-access-token" || r.Header.Get("Content-Type") != "application/json; charset=UTF-8" {
					t.Fatal("incorrect request headers")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != `{"push_registration":{"service":"google_cloud_messaging","registration_id":"test-fcm-token"}}` {
					t.Fatalf("incorrect APK registration contract: %s (%v)", body, err)
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			err := RegisterFCMPush(context.Background(), "test-access-token", "test-fcm-token")
			if calls != 1 || (err == nil) != tc.ok {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			if err != nil && (strings.Contains(err.Error(), "test-access-token") || strings.Contains(err.Error(), "test-fcm-token")) {
				t.Fatal("registration error exposes a token")
			}
		})
	}
}

func TestFCMPushRegistrationValidation(t *testing.T) {
	previous := pushHTTPClient
	t.Cleanup(func() { pushHTTPClient = previous })
	pushHTTPClient = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		t.Fatal("invalid registration made a request")
		return nil, nil
	})}
	for _, tokens := range [][2]string{{"", "push"}, {"access", ""}, {"access", " \n\t"}} {
		if err := RegisterFCMPush(context.Background(), tokens[0], tokens[1]); err == nil {
			t.Fatal("expected validation error")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RegisterFCMPush(ctx, "access", "push"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestFCMPushRegistrationDoesNotFollowRedirects(t *testing.T) {
	client := *pushHTTPClient
	previous := pushHTTPClient
	t.Cleanup(func() { pushHTTPClient = previous })
	calls := 0
	client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			t.Fatal("followed a redirect with push credentials")
		}
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": {"https://other.example/push"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})
	pushHTTPClient = &client
	if err := RegisterFCMPush(context.Background(), "access", "push"); err == nil || calls != 1 {
		t.Fatalf("expected redirect failure, calls=%d error=%v", calls, err)
	}
}

func TestFCMPushRegistrationTransportError(t *testing.T) {
	previous := pushHTTPClient
	t.Cleanup(func() { pushHTTPClient = previous })
	pushHTTPClient = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Post", URL: "https://v2.groupme.com/?token=secret", Err: context.DeadlineExceeded}
	})}
	err := RegisterFCMPush(context.Background(), "access", "push")
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("transport error lost cause or exposed credentials: %v", err)
	}
}

func TestFCMPushRegistrationCancelsRequest(t *testing.T) {
	previous := pushHTTPClient
	t.Cleanup(func() { pushHTTPClient = previous })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushHTTPClient = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		cancel()
		return nil, r.Context().Err()
	})}
	if err := RegisterFCMPush(ctx, "access", "push"); !errors.Is(err, context.Canceled) {
		t.Fatalf("request did not inherit cancellation: %v", err)
	}
}
