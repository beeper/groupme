// Package groupme defines a client capable of executing API commands for the GroupMe chat service
package groupme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"
)

// GroupMeAPIBase - Endpoints are added on to this to get the full URI.
// Overridable for testing
const GroupMeAPIBase = "https://api.groupme.com/v3"

// Client communicates with the GroupMe API to perform actions
// on the basic types, i.e. Listing, Creating, Destroying
type Client struct {
	httpClient         *http.Client
	endpointBase       string
	authorizationToken string
}

// NewClient creates a new GroupMe API Client
func NewClient(authToken string) *Client {
	return &Client{
		// TODO: enable transport information passing in
		httpClient:         &http.Client{Timeout: 30 * time.Second},
		endpointBase:       GroupMeAPIBase,
		authorizationToken: authToken,
	}
}

// Close safely shuts down the Client
func (c *Client) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

// String returns a json formatted string
func (c Client) String() string {
	return marshal(&c)
}

/*/// Handle parsing of nested interface type response ///*/
type jsonResponse struct {
	Response response `json:"response"`
	Meta     `json:"meta"`
}

func newJSONResponse(i interface{}) *jsonResponse {
	return &jsonResponse{Response: response{i}}
}

type response struct {
	i interface{}
}

func (r response) UnmarshalJSON(bs []byte) error {
	return json.NewDecoder(bytes.NewBuffer(bs)).Decode(r.i)
}

const errorStatusCodeMin = 300

func (c Client) do(ctx context.Context, req *http.Request, i interface{}) error {
	req = req.WithContext(ctx)
	if req.Method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}

	getResp, err := c.httpClient.Do(req)
	if err != nil {
		// net/http's *url.Error embeds the full request URL, which
		// doWithAuthToken put the access token into as a query parameter --
		// so any transport error (timeout, connection reset) would otherwise
		// leak the token into the bridge's logs verbatim. Drop the query.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			if u, perr := url.Parse(urlErr.URL); perr == nil {
				u.RawQuery = ""
				urlErr.URL = u.String()
			}
		}
		return err
	}
	defer getResp.Body.Close()

	var readBytes []byte
	// Check Status Code is 1XX or 2XX
	if getResp.StatusCode >= errorStatusCodeMin {
		readBytes, err = ioutil.ReadAll(getResp.Body)
		if err != nil {
			// We couldn't read the output.  Oh well; generate the appropriate error type anyway.
			return &Meta{
				Code: HTTPStatusCode(getResp.StatusCode),
			}
		}

		resp := newJSONResponse(nil)
		if err = json.Unmarshal(readBytes, &resp); err != nil {
			// We couldn't parse the output.  Oh well; generate the appropriate error type anyway.
			return &Meta{
				Code: HTTPStatusCode(getResp.StatusCode),
			}
		}
		return &resp.Meta
	}

	if i == nil {
		return nil
	}

	readBytes, err = ioutil.ReadAll(getResp.Body)
	if err != nil {
		return err
	}

	resp := newJSONResponse(i)
	if err := json.Unmarshal(readBytes, &resp); err != nil {
		return err
	}

	return nil
}

func (c Client) doWithAuthToken(ctx context.Context, req *http.Request, i interface{}) error {
	req.Header.Set("X-Access-Token", c.authorizationToken)

	return c.do(ctx, req, i)
}
