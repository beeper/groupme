package connector

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
)

type loginTransport func(*http.Request) (*http.Response, error)

func (f loginTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLoginRejectsInvalidCredentialsWithoutSaving(t *testing.T) {
	for _, flow := range []string{LoginFlowIDWeb, LoginFlowIDToken} {
		for _, body := range []string{`{"meta":{"code":401,"errors":["secret-token"]}}`, `{"response":{"name":"No ID"},"meta":{"code":200}}`} {
			t.Run(flow+body, func(t *testing.T) {
				previous := http.DefaultTransport
				http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("X-Access-Token") != "secret-token" || r.URL.Query().Has("token") {
						t.Error("token must only be sent in the authentication header")
					}
					code := 200
					if strings.Contains(body, "401") {
						code = 401
					}
					return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})
				t.Cleanup(func() { http.DefaultTransport = previous })
				process, err := (&GMConnector{}).CreateLogin(context.Background(), nil, flow)
				if err != nil {
					t.Fatal(err)
				}
				defer process.Cancel()
				step, err := process.Start(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if flow == LoginFlowIDWeb && step.Type != bridgev2.LoginStepTypeCookies {
					t.Fatal("webview not offered")
				}
				var result *bridgev2.LoginStep
				if flow == LoginFlowIDWeb {
					result, err = process.(bridgev2.LoginProcessCookies).SubmitCookies(context.Background(), map[string]string{"token": "secret-token"})
				} else {
					result, err = process.(bridgev2.LoginProcessUserInput).SubmitUserInput(context.Background(), map[string]string{"token": "secret-token"})
				}
				if result != nil || err == nil || strings.Contains(err.Error(), "secret-token") {
					t.Fatalf("unsafe login result: %v, %v", result, err)
				}
			})
		}
	}
}

func TestCancelStopsLoginValidation(t *testing.T) {
	started := make(chan struct{})
	previous := http.DefaultTransport
	http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	process, err := (&GMConnector{}).CreateLogin(context.Background(), nil, LoginFlowIDWeb)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := process.(bridgev2.LoginProcessCookies).SubmitCookies(context.Background(), map[string]string{"token": "secret-token"})
		done <- err
	}()
	<-started
	process.Cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := process.Start(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled login restarted: %v", err)
	}
}
