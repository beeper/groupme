package connector

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
)

type loginTransport func(*http.Request) (*http.Response, error)

func (f loginTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLoginRemoteResponses(t *testing.T) {
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
