package groupmeext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Password sign-in as the GroupMe Android app (com.groupme.android 16.36.2)
// does it. Its LoginRequest signs the request with a hash of a constant key,
// the app ID, the device ID and the username in X-Access-Token instead of the
// web client's Arkose captcha token. Web tokens expire 14 days after sign-in.
const (
	androidVersionCode = "263140604"
	androidAppID       = "Android-" + androidVersionCode
	androidLoginKey    = "48ea3317-4a12-4a30-9b87-efdf2dc1b9ec"
	androidUserAgent   = "Dalvik/2.1.0 (Linux; U; Android 14; Pixel 8 Build/AP2A.240905.003)"
)

// GroupMe keeps one token per user, app ID and device ID, so a stable device
// ID stops repeated sign-ins from piling up entries in the account's device list.
func AndroidDeviceID(matrixUser, username string) string {
	sum := sha256.Sum256([]byte("groupme-android-device\x00" + matrixUser + "\x00" + strings.ToLower(username)))
	return hex.EncodeToString(sum[:8])
}

func androidLoginSignature(deviceID, username string) string {
	sum := sha256.Sum256([]byte(androidLoginKey + androidAppID + deviceID + username))
	return base64.StdEncoding.EncodeToString(sum[:])
}

type PasswordLoginResult struct {
	AccessToken  string `json:"access_token"`
	Verification *struct {
		Code string `json:"code"`
	} `json:"verification"`
}

type AuthError struct {
	Request    string
	StatusCode int
	MetaCode   int
	Errors     []string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("%s: HTTP %d (code %d, %v)", e.Request, e.StatusCode, e.MetaCode, e.Errors)
}

type authEnvelope struct {
	Response json.RawMessage `json:"response"`
	Meta     struct {
		Code   int      `json:"code"`
		Errors []string `json:"errors"`
	} `json:"meta"`
}

func authRequest(ctx context.Context, name, endpoint string, header http.Header, body, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	for key, values := range header {
		req.Header[key] = values
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", androidUserAgent)
	resp, err := activityHTTPClient.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	defer resp.Body.Close()
	var env authEnvelope
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&env)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &AuthError{Request: name, StatusCode: resp.StatusCode, MetaCode: env.Meta.Code, Errors: env.Meta.Errors}
	} else if decodeErr != nil {
		return fmt.Errorf("%s: invalid response: %w", name, decodeErr)
	} else if result != nil && len(env.Response) > 0 {
		if err = json.Unmarshal(env.Response, result); err != nil {
			return fmt.Errorf("%s: invalid response: %w", name, err)
		}
	}
	return nil
}

// PasswordLogin requests an access token for the Android app ID. A result
// with Verification set and no AccessToken means the account has two-step
// verification on: confirm a PIN for Verification.Code, then call this again
// with that code.
func PasswordLogin(ctx context.Context, username, password, deviceID, verificationCode string) (*PasswordLoginResult, error) {
	body := map[string]any{
		"username":    username,
		"password":    password,
		"grant_type":  "password",
		"app_id":      androidAppID,
		"app_version": androidVersionCode,
		"device_id":   deviceID,
	}
	if verificationCode != "" {
		body["verification"] = map[string]string{"code": verificationCode}
	}
	header := http.Header{"X-Access-Token": {androidLoginSignature(deviceID, username)}}
	var result PasswordLoginResult
	if err := authRequest(ctx, "GroupMe sign-in", "https://v2.groupme.com/access_tokens", header, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func InitiateVerification(ctx context.Context, code string) (string, error) {
	var result struct {
		Hint string `json:"hint"`
	}
	body := map[string]any{"verification": map[string]string{"method": "sms"}}
	err := authRequest(ctx, "GroupMe verification", "https://api.groupme.com/v3/verifications/"+url.PathEscape(code)+"/initiate", nil, body, &result)
	return result.Hint, err
}

const VerificationIncorrectPIN = 40002

// ConfirmVerification only reports success; the app then repeats the sign-in
// with the verification code.
func ConfirmVerification(ctx context.Context, code, pin string) error {
	body := map[string]any{"verification": map[string]string{"pin": pin}}
	return authRequest(ctx, "GroupMe verification", "https://api.groupme.com/v3/verifications/"+url.PathEscape(code)+"/confirm", nil, body, nil)
}
