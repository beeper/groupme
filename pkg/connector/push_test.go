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

func TestPushRegistrationFromWrapper(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	calls := 0
	http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || r.URL.String() != "https://v2.groupme.com/push_registrations" || r.Header.Get("X-Access-Token") != "test-token" || !strings.Contains(string(body), `"registration_id":"wrapper-fcm-token"`) {
			t.Fatalf("incorrect registration request: %s %s %s (%v)", r.Method, r.URL, body, err)
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{"response":{"push_registration":{"service":"google_cloud_messaging","registration_id":"wrapper-fcm-token"}}}`))}, nil
	})
	gc := dmTestClient()
	var pushable bridgev2.PushableNetworkAPI = gc
	cfg := pushable.GetPushConfigs()
	if cfg.FCM == nil || cfg.FCM.SenderID != "836136099376" || cfg.Web != nil || cfg.APNs != nil || cfg.Native {
		t.Fatalf("incorrect push relay configuration: %+v", cfg)
	}
	if err := pushable.RegisterPushNotifications(context.Background(), bridgev2.PushTypeFCM, "wrapper-fcm-token"); err != nil || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestPushRegistrationRejectsUnsupportedRequests(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("unsupported push registration made a request")
		return nil, nil
	})
	gc := dmTestClient()
	for _, pushType := range []bridgev2.PushType{bridgev2.PushTypeUnknown, bridgev2.PushTypeWeb, bridgev2.PushTypeAPNs} {
		if err := gc.RegisterPushNotifications(context.Background(), pushType, "token"); err == nil {
			t.Fatalf("accepted unsupported push type %s", pushType)
		}
	}
	gc.Client = nil
	if err := gc.RegisterPushNotifications(context.Background(), bridgev2.PushTypeFCM, "token"); !errors.Is(err, bridgev2.ErrNotLoggedIn) {
		t.Fatalf("expected logged-out error, got %v", err)
	}
}
