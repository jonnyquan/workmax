//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLoginTransactionClient_RejectsMalformedCreateResponses(t *testing.T) {
	transactionID := testLoginToken('i', loginTransactionIDBytes)
	transactionSecret := testLoginToken('t', loginTransactionCapabilityBytes)
	rawMarker := "RAW_CREATE_RESPONSE_SECRET"
	validBody := `{"transaction_id":"` + transactionID + `","transaction_secret":"` + transactionSecret +
		`","expires_at":"` + testLoginExpires + `","methods":["password"],"debug":"` + rawMarker + `"}`

	tests := []struct {
		name         string
		contentTypes []string
		body         string
	}{
		{name: "missing Content-Type", body: validBody},
		{name: "wrong Content-Type", contentTypes: []string{"text/plain"}, body: validBody},
		{name: "duplicate Content-Type", contentTypes: []string{"application/json", "application/json"}, body: validBody},
		{name: "unexpected Content-Type parameter", contentTypes: []string{"application/json; charset=utf-8; profile=internal"}, body: validBody},
		{name: "oversized Content-Type", contentTypes: []string{"application/json; profile=" + strings.Repeat("x", loginTransactionMaxContentTypeBytes)}, body: validBody},
		{
			name:         "case-folded known field alias",
			contentTypes: []string{"application/json"},
			body: `{"Transaction_ID":"` + transactionID + `","transaction_secret":"` + transactionSecret +
				`","expires_at":"` + testLoginExpires + `","methods":["password"],"debug":"` + rawMarker + `"}`,
		},
		{
			name:         "canonical field plus case-folded alias",
			contentTypes: []string{"application/json"},
			body: `{"transaction_id":"` + transactionID + `","Transaction_ID":"` + testLoginToken('z', loginTransactionIDBytes) +
				`","transaction_secret":"` + transactionSecret + `","expires_at":"` + testLoginExpires +
				`","methods":["password"],"debug":"` + rawMarker + `"}`,
		},
		{
			name:         "duplicate transaction ID",
			contentTypes: []string{"application/json"},
			body: `{"transaction_id":"` + transactionID + `","transaction_id":"` + testLoginToken('z', loginTransactionIDBytes) +
				`","transaction_secret":"` + transactionSecret + `","expires_at":"` + testLoginExpires +
				`","methods":["password"],"debug":"` + rawMarker + `"}`,
		},
		{
			name:         "duplicate transaction capability",
			contentTypes: []string{"application/json"},
			body: `{"transaction_id":"` + transactionID + `","transaction_secret":"` + transactionSecret +
				`","transaction_secret":"` + testLoginToken('z', loginTransactionCapabilityBytes) + `","expires_at":"` +
				testLoginExpires + `","methods":["password"],"debug":"` + rawMarker + `"}`,
		},
		{
			name:         "padded transaction ID",
			contentTypes: []string{"application/json"},
			body: `{"transaction_id":"` + transactionID + `=","transaction_secret":"` + transactionSecret +
				`","expires_at":"` + testLoginExpires + `","methods":["password"],"debug":"` + rawMarker + `"}`,
		},
		{
			name:         "malformed transaction capability",
			contentTypes: []string{"application/json"},
			body: `{"transaction_id":"` + transactionID + `","transaction_secret":"not-canonical","expires_at":"` +
				testLoginExpires + `","methods":["password"],"debug":"` + rawMarker + `"}`,
		},
		{
			name:         "missing expiry",
			contentTypes: []string{"application/json"},
			body: `{"transaction_id":"` + transactionID + `","transaction_secret":"` + transactionSecret +
				`","methods":["password"],"debug":"` + rawMarker + `"}`,
		},
		{
			name:         "password method absent",
			contentTypes: []string{"application/json"},
			body: `{"transaction_id":"` + transactionID + `","transaction_secret":"` + transactionSecret +
				`","expires_at":"` + testLoginExpires + `","methods":["google"],"debug":"` + rawMarker + `"}`,
		},
		{
			name:         "duplicate method",
			contentTypes: []string{"application/json"},
			body: `{"transaction_id":"` + transactionID + `","transaction_secret":"` + transactionSecret +
				`","expires_at":"` + testLoginExpires + `","methods":["password","password"],"debug":"` + rawMarker + `"}`,
		},
		{
			name:         "non-canonical method",
			contentTypes: []string{"application/json"},
			body: `{"transaction_id":"` + transactionID + `","transaction_secret":"` + transactionSecret +
				`","expires_at":"` + testLoginExpires + `","methods":["Password"],"debug":"` + rawMarker + `"}`,
		},
		{name: "top-level array", contentTypes: []string{"application/json"}, body: `[{"debug":"` + rawMarker + `"}]`},
		{name: "trailing JSON", contentTypes: []string{"application/json"}, body: validBody + ` {"raw":"` + rawMarker + `"}`},
		{name: "invalid UTF-8", contentTypes: []string{"application/json"}, body: validBody[:len(validBody)-1] + `,"future":"` + string([]byte{0xff}) + `"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, value := range test.contentTypes {
					w.Header().Add("Content-Type", value)
				}
				w.Header().Set("X-Internal-Debug", rawMarker)
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, test.body)
			}))
			defer upstream.Close()

			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()
			_, err := client.CreateLoginTransaction(
				context.Background(),
				validLoginTransactionCreateInputForTest("http://127.0.0.1:49152/oauth/callback"),
			)
			if !errors.Is(err, ErrLoginTransactionProtocol) {
				t.Fatalf("error = %v, want ErrLoginTransactionProtocol", err)
			}
			assertLoginTransactionErrorRedacted(t, err, rawMarker, transactionSecret)
		})
	}
}

func TestLoginTransactionClient_ValidatesStatusAndPasswordResponseFields(t *testing.T) {
	transactionID := testLoginToken('i', loginTransactionIDBytes)
	transactionSecret := testLoginToken('t', loginTransactionCapabilityBytes)
	exchangeToken := testLoginToken('x', loginTransactionCapabilityBytes)
	rawMarker := "RAW_TYPED_RESPONSE_SECRET"

	tests := []struct {
		name   string
		status int
		body   string
		invoke func(*Client) error
	}{
		{
			name:   "status mismatched transaction ID",
			status: http.StatusOK,
			body: `{"transaction_id":"` + testLoginToken('z', loginTransactionIDBytes) +
				`","version":1,"state":"pending","expires_at":"` + testLoginExpires + `","updated_at":"` + testLoginUpdated + `","debug":"` + rawMarker + `"}`,
			invoke: func(client *Client) error {
				_, err := client.InspectLoginTransaction(context.Background(), transactionID, transactionSecret)
				return err
			},
		},
		{
			name:   "status zero version",
			status: http.StatusOK,
			body: `{"transaction_id":"` + transactionID +
				`","version":0,"state":"pending","expires_at":"` + testLoginExpires + `","updated_at":"` + testLoginUpdated + `","debug":"` + rawMarker + `"}`,
			invoke: func(client *Client) error {
				_, err := client.InspectLoginTransaction(context.Background(), transactionID, transactionSecret)
				return err
			},
		},
		{
			name:   "status unknown state",
			status: http.StatusOK,
			body: `{"transaction_id":"` + transactionID +
				`","version":1,"state":"RAW_UNKNOWN_STATE","expires_at":"` + testLoginExpires + `","updated_at":"` + testLoginUpdated + `","debug":"` + rawMarker + `"}`,
			invoke: func(client *Client) error {
				_, err := client.InspectLoginTransaction(context.Background(), transactionID, transactionSecret)
				return err
			},
		},
		{
			name:   "status missing timestamp",
			status: http.StatusOK,
			body: `{"transaction_id":"` + transactionID +
				`","version":1,"state":"pending","expires_at":"` + testLoginExpires + `","debug":"` + rawMarker + `"}`,
			invoke: func(client *Client) error {
				_, err := client.InspectLoginTransaction(context.Background(), transactionID, transactionSecret)
				return err
			},
		},
		{
			name:   "status case-folded known alias",
			status: http.StatusOK,
			body: `{"transaction_id":"` + transactionID +
				`","Version":1,"state":"pending","expires_at":"` + testLoginExpires + `","updated_at":"` + testLoginUpdated + `","debug":"` + rawMarker + `"}`,
			invoke: func(client *Client) error {
				_, err := client.InspectLoginTransaction(context.Background(), transactionID, transactionSecret)
				return err
			},
		},
		{
			name:   "password mismatched transaction ID",
			status: http.StatusOK,
			body: `{"transaction_id":"` + testLoginToken('z', loginTransactionIDBytes) + `","exchange_token":"` + exchangeToken +
				`","expires_at":"` + testLoginExpires + `","debug":"` + rawMarker + `"}`,
			invoke: func(client *Client) error {
				_, err := client.CompleteLoginTransactionPassword(context.Background(), LoginTransactionPasswordInput{
					TransactionID:     transactionID,
					TransactionSecret: transactionSecret,
					Email:             "person@example.com",
					Password:          "correct-password",
				})
				return err
			},
		},
		{
			name:   "password malformed exchange capability",
			status: http.StatusOK,
			body: `{"transaction_id":"` + transactionID + `","exchange_token":"not-canonical","expires_at":"` +
				testLoginExpires + `","debug":"` + rawMarker + `"}`,
			invoke: func(client *Client) error {
				_, err := client.CompleteLoginTransactionPassword(context.Background(), LoginTransactionPasswordInput{
					TransactionID: transactionID, TransactionSecret: transactionSecret,
					Email: "person@example.com", Password: "correct-password",
				})
				return err
			},
		},
		{
			name:   "password missing expiry",
			status: http.StatusOK,
			body:   `{"transaction_id":"` + transactionID + `","exchange_token":"` + exchangeToken + `","debug":"` + rawMarker + `"}`,
			invoke: func(client *Client) error {
				_, err := client.CompleteLoginTransactionPassword(context.Background(), LoginTransactionPasswordInput{
					TransactionID: transactionID, TransactionSecret: transactionSecret,
					Email: "person@example.com", Password: "correct-password",
				})
				return err
			},
		},
		{
			name:   "password duplicate exchange capability",
			status: http.StatusOK,
			body: `{"transaction_id":"` + transactionID + `","exchange_token":"` + exchangeToken + `","exchange_token":"` +
				testLoginToken('z', loginTransactionCapabilityBytes) + `","expires_at":"` + testLoginExpires + `","debug":"` + rawMarker + `"}`,
			invoke: func(client *Client) error {
				_, err := client.CompleteLoginTransactionPassword(context.Background(), LoginTransactionPasswordInput{
					TransactionID: transactionID, TransactionSecret: transactionSecret,
					Email: "person@example.com", Password: "correct-password",
				})
				return err
			},
		},
		{
			name:   "password case-folded known alias",
			status: http.StatusOK,
			body: `{"transaction_id":"` + transactionID + `","Exchange_Token":"` + exchangeToken +
				`","expires_at":"` + testLoginExpires + `","debug":"` + rawMarker + `"}`,
			invoke: func(client *Client) error {
				_, err := client.CompleteLoginTransactionPassword(context.Background(), LoginTransactionPasswordInput{
					TransactionID: transactionID, TransactionSecret: transactionSecret,
					Email: "person@example.com", Password: "correct-password",
				})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Internal-Debug", rawMarker)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer upstream.Close()
			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()

			err := test.invoke(client)
			if !errors.Is(err, ErrLoginTransactionProtocol) {
				t.Fatalf("error = %v, want ErrLoginTransactionProtocol", err)
			}
			assertLoginTransactionErrorRedacted(t, err, rawMarker, transactionSecret, exchangeToken, "RAW_UNKNOWN_STATE")
		})
	}
}

func TestLoginTransactionClient_BoundsResponseBodiesAndReadErrors(t *testing.T) {
	rawMarker := "RAW_BOUNDED_BODY_SECRET"
	tests := []struct {
		name          string
		contentLength int64
		body          io.ReadCloser
		want          error
	}{
		{
			name:          "declared oversized body",
			contentLength: loginTransactionMaxResponseBodyBytes + 1,
			body:          io.NopCloser(strings.NewReader(`{}`)),
			want:          ErrLoginTransactionProtocol,
		},
		{
			name:          "invalid negative Content-Length",
			contentLength: -2,
			body:          io.NopCloser(strings.NewReader(`{}`)),
			want:          ErrLoginTransactionProtocol,
		},
		{
			name:          "streamed oversized body",
			contentLength: -1,
			body:          io.NopCloser(strings.NewReader(strings.Repeat(rawMarker, loginTransactionMaxResponseBodyBytes/len(rawMarker)+2))),
			want:          ErrLoginTransactionProtocol,
		},
		{
			name:          "declared length mismatch",
			contentLength: 128,
			body:          io.NopCloser(strings.NewReader(`{}`)),
			want:          ErrLoginTransactionProtocol,
		},
		{
			name:          "body read failure",
			contentLength: -1,
			body:          io.NopCloser(loginTransactionFailingReader{err: errors.New(rawMarker)}),
			want:          ErrLoginTransactionTransport,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client := NewClient("https://go-server.invalid")
			client.HTTPClient = &http.Client{Transport: loginTransactionRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{
					StatusCode:    http.StatusCreated,
					Header:        http.Header{"Content-Type": []string{"application/json"}, "X-Raw-Internal": []string{rawMarker}},
					Body:          test.body,
					ContentLength: test.contentLength,
					Request:       request,
				}, nil
			})}

			_, err := client.CreateLoginTransaction(
				context.Background(),
				validLoginTransactionCreateInputForTest("http://127.0.0.1:49152/oauth/callback"),
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if calls.Load() != 1 {
				t.Fatalf("RoundTrip calls = %d, want 1", calls.Load())
			}
			assertLoginTransactionErrorRedacted(t, err, rawMarker)
		})
	}
}

type loginTransactionFailingReader struct {
	err error
}

func (r loginTransactionFailingReader) Read(_ []byte) (int, error) {
	return 0, r.err
}

func TestLoginTransactionClient_ServerErrorsAreClosedAndRedacted(t *testing.T) {
	transactionSecret := testLoginToken('t', loginTransactionCapabilityBytes)
	exchangeToken := testLoginToken('x', loginTransactionCapabilityBytes)
	password := "correct-password"
	rawMarker := "RAW_SERVER_ERROR_SECRET"

	tests := []struct {
		name         string
		status       int
		contentTypes []string
		body         string
		wantCode     string
		wantProtocol bool
	}{
		{
			name: "known error", status: http.StatusUnauthorized, contentTypes: []string{"application/json"},
			body: `{"error":"invalid_credentials","detail":"` + rawMarker + `"}`, wantCode: "invalid_credentials",
		},
		{
			name: "unknown code", status: http.StatusServiceUnavailable, contentTypes: []string{"application/json"},
			body: `{"error":"` + rawMarker + `"}`, wantCode: "upstream_rejected",
		},
		{
			name: "rate limiter envelope", status: http.StatusTooManyRequests, contentTypes: []string{"application/json; charset=utf-8"},
			body: `{"code":429,"message":"` + rawMarker + `"}`, wantCode: "rate_limited",
		},
		{
			name: "case-folded error alias", status: http.StatusUnauthorized, contentTypes: []string{"application/json"},
			body: `{"Error":"invalid_credentials","detail":"` + rawMarker + `"}`, wantProtocol: true,
		},
		{
			name: "known code on wrong status", status: http.StatusInternalServerError, contentTypes: []string{"application/json"},
			body: `{"error":"invalid_credentials","detail":"` + rawMarker + `"}`, wantProtocol: true,
		},
		{
			name: "unexpected success status", status: http.StatusCreated, contentTypes: []string{"application/json"},
			body: `{"error":"invalid_credentials","detail":"` + rawMarker + `"}`, wantProtocol: true,
		},
		{
			name: "duplicate error code", status: http.StatusUnauthorized, contentTypes: []string{"application/json"},
			body: `{"error":"invalid_credentials","error":"identity_unavailable","detail":"` + rawMarker + `"}`, wantProtocol: true,
		},
		{
			name: "non-JSON error", status: http.StatusUnauthorized, contentTypes: []string{"text/plain"},
			body: rawMarker, wantProtocol: true,
		},
		{
			name: "duplicate error Content-Type", status: http.StatusUnauthorized,
			contentTypes: []string{"application/json", "application/json"},
			body:         `{"error":"invalid_credentials","detail":"` + rawMarker + `"}`, wantProtocol: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				for _, value := range test.contentTypes {
					w.Header().Add("Content-Type", value)
				}
				w.Header().Set("X-Raw-Internal", rawMarker)
				w.Header().Set("Retry-After", rawMarker)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer upstream.Close()

			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()
			_, err := client.CompleteLoginTransactionPassword(context.Background(), LoginTransactionPasswordInput{
				TransactionID:     testLoginToken('i', loginTransactionIDBytes),
				TransactionSecret: transactionSecret,
				Email:             "person@example.com",
				Password:          password,
			})
			if test.wantProtocol {
				if !errors.Is(err, ErrLoginTransactionProtocol) {
					t.Fatalf("error = %v, want ErrLoginTransactionProtocol", err)
				}
			} else {
				var serverError *LoginTransactionServerError
				if !errors.As(err, &serverError) {
					t.Fatalf("error = %v, want LoginTransactionServerError", err)
				}
				if serverError.Operation != "password" || serverError.StatusCode != test.status || serverError.Code != test.wantCode {
					t.Fatalf("server error = %+v", serverError)
				}
			}
			assertLoginTransactionErrorRedacted(t, err, rawMarker, transactionSecret, exchangeToken, password)
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want exactly 1", requests.Load())
			}
		})
	}
}

func TestLoginTransactionClient_RejectsUnboundExchangeLocationsWithoutFollowing(t *testing.T) {
	var callbackRequests atomic.Int32
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callbackRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer callback.Close()

	expectedRedirect := callback.URL + "/oauth/callback"
	parsedCallback, err := url.Parse(callback.URL)
	if err != nil {
		t.Fatal(err)
	}
	wrongPort := "1"
	if parsedCallback.Port() == wrongPort {
		wrongPort = "2"
	}
	code := testLoginToken('c', loginTransactionAuthCodeBytes)
	state := testLoginToken('s', loginTransactionOAuthStateBytes)
	wrongState := testLoginToken('z', loginTransactionOAuthStateBytes)
	validLocation := expectedRedirect + "?code=" + code + "&state=" + state
	rawMarker := "RAW_LOCATION_HEADER_SECRET"

	tests := []struct {
		name      string
		locations []string
		body      string
	}{
		{name: "missing Location"},
		{name: "duplicate Location", locations: []string{validLocation, validLocation}},
		{name: "relative Location", locations: []string{"/oauth/callback?code=" + code + "&state=" + state}},
		{name: "HTTPS scheme", locations: []string{strings.Replace(validLocation, "http://", "https://", 1)}},
		{name: "localhost host", locations: []string{strings.Replace(validLocation, "127.0.0.1", "localhost", 1)}},
		{name: "wrong port", locations: []string{"http://127.0.0.1:" + wrongPort + "/oauth/callback?code=" + code + "&state=" + state}},
		{name: "missing port", locations: []string{"http://127.0.0.1/oauth/callback?code=" + code + "&state=" + state}},
		{name: "userinfo", locations: []string{strings.Replace(validLocation, "http://", "http://user@", 1)}},
		{name: "wrong path", locations: []string{callback.URL + "/other?code=" + code + "&state=" + state}},
		{name: "escaped path", locations: []string{callback.URL + "/oauth%2Fcallback?code=" + code + "&state=" + state}},
		{name: "fragment", locations: []string{validLocation + "#" + rawMarker}},
		{name: "empty fragment", locations: []string{validLocation + "#"}},
		{name: "wrong state", locations: []string{expectedRedirect + "?code=" + code + "&state=" + wrongState}},
		{name: "missing code", locations: []string{expectedRedirect + "?state=" + state}},
		{name: "padded code", locations: []string{expectedRedirect + "?code=" + code + "=&state=" + state}},
		{name: "malformed escaped code", locations: []string{expectedRedirect + "?code=%ZZ&state=" + state}},
		{name: "percent-normalized code", locations: []string{expectedRedirect + "?code=%59" + code[1:] + "&state=" + state}},
		{name: "duplicate code", locations: []string{validLocation + "&code=" + code}},
		{name: "duplicate state", locations: []string{validLocation + "&state=" + state}},
		{name: "extra query key", locations: []string{validLocation + "&debug=" + rawMarker}},
		{name: "reordered query", locations: []string{expectedRedirect + "?state=" + state + "&code=" + code}},
		{name: "oversized Location", locations: []string{validLocation + strings.Repeat("x", loginTransactionMaxLocationBytes)}},
		{name: "oversized redirect body", locations: []string{validLocation}, body: strings.Repeat(rawMarker, loginTransactionMaxResponseBodyBytes/len(rawMarker)+2)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := callbackRequests.Load()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, location := range test.locations {
					w.Header().Add("Location", location)
				}
				w.Header().Set("X-Raw-Internal", rawMarker)
				w.WriteHeader(http.StatusSeeOther)
				_, _ = io.WriteString(w, test.body)
			}))
			defer upstream.Close()

			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()
			input := LoginTransactionExchangeInput{
				TransactionID:       testLoginToken('i', loginTransactionIDBytes),
				ExchangeToken:       testLoginToken('x', loginTransactionCapabilityBytes),
				ExpectedRedirectURI: expectedRedirect,
				ExpectedOAuthState:  state,
			}
			_, err := client.ExchangeLoginTransaction(context.Background(), input)
			if !errors.Is(err, ErrLoginTransactionProtocol) {
				t.Fatalf("error = %v, want ErrLoginTransactionProtocol", err)
			}
			if callbackRequests.Load() != before {
				t.Fatalf("invalid 303 was followed: before=%d after=%d", before, callbackRequests.Load())
			}
			for _, location := range test.locations {
				assertLoginTransactionErrorRedacted(t, err, location)
			}
			assertLoginTransactionErrorRedacted(t, err, rawMarker, code, state, wrongState, input.ExchangeToken)
		})
	}
}

func TestValidatedLoginTransactionLocationRejectsUnnormalizedHeaderValues(t *testing.T) {
	expected, ok := parseCanonicalLoginLoopback("http://127.0.0.1:49152/oauth/callback")
	if !ok {
		t.Fatal("test callback should be canonical")
	}
	code := testLoginToken('c', loginTransactionAuthCodeBytes)
	state := testLoginToken('s', loginTransactionOAuthStateBytes)
	valid := expected.String() + "?code=" + code + "&state=" + state

	for _, raw := range []string{
		" " + valid,
		valid + " ",
		valid + "#",
		valid + "#fragment",
		valid + strings.Repeat("x", loginTransactionMaxLocationBytes),
	} {
		header := http.Header{"Location": []string{raw}}
		if got, ok := validatedLoginTransactionLocation(header, expected, state); ok || got != "" {
			t.Fatalf("accepted Location %q", raw)
		}
	}
}

func TestLoginTransactionClient_DoesNotFollowRedirectsFromJSONEndpoints(t *testing.T) {
	var callbackRequests atomic.Int32
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callbackRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer callback.Close()

	upstreamRequests := atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		w.Header().Set("Location", callback.URL)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(w, `{"error":"invalid_request"}`)
	}))
	defer upstream.Close()

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	_, err := client.CreateLoginTransaction(
		context.Background(),
		validLoginTransactionCreateInputForTest("http://127.0.0.1:49152/oauth/callback"),
	)
	if !errors.Is(err, ErrLoginTransactionProtocol) {
		t.Fatalf("error = %v, want ErrLoginTransactionProtocol for 307", err)
	}
	if upstreamRequests.Load() != 1 || callbackRequests.Load() != 0 {
		t.Fatalf("requests upstream=%d callback=%d, want 1/0", upstreamRequests.Load(), callbackRequests.Load())
	}
}

func TestReadBoundedLoginTransactionBody_StrictFramingEdges(t *testing.T) {
	// ContentLength == -1 is the only accepted negative value used by net/http
	// for an unknown body length; the streaming cap still applies in that case.
	oversized := &http.Response{
		Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", loginTransactionMaxResponseBodyBytes+1))),
		ContentLength: -1,
	}
	if _, err := readBoundedLoginTransactionBody(oversized); !errors.Is(err, ErrLoginTransactionProtocol) {
		t.Fatalf("error = %v, want ErrLoginTransactionProtocol (cap %s)", err, strconv.Itoa(loginTransactionMaxResponseBodyBytes))
	}

	exactBody := strings.Repeat("x", loginTransactionMaxResponseBodyBytes)
	exact := &http.Response{Body: io.NopCloser(strings.NewReader(exactBody)), ContentLength: int64(len(exactBody))}
	got, err := readBoundedLoginTransactionBody(exact)
	if err != nil || string(got) != exactBody {
		t.Fatalf("exact-cap body: len=%d err=%v", len(got), err)
	}

	if _, err := readBoundedLoginTransactionBody(&http.Response{}); !errors.Is(err, ErrLoginTransactionProtocol) {
		t.Fatalf("nil body error = %v, want ErrLoginTransactionProtocol", err)
	}
}

func TestLoginTransactionJSONContentType_StrictSyntaxAndCap(t *testing.T) {
	for _, raw := range []string{
		" application/json",
		"application/json ",
		"application/json\x00",
		"application/json; charset=latin1",
		"application/json; profile=" + strings.Repeat("x", loginTransactionMaxContentTypeBytes),
		string([]byte{'a', 'p', 'p', 0xff}),
	} {
		if isLoginTransactionJSONContentType(http.Header{"Content-Type": []string{raw}}) {
			t.Fatalf("accepted Content-Type %q", raw)
		}
	}
}

func TestLoginTransactionClient_CanceledContextIsStableTransportError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient("https://go-server.invalid")
	client.HTTPClient = &http.Client{Transport: loginTransactionRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("RAW_CONTEXT_TRANSPORT_SECRET")
	})}

	_, err := client.CreateLoginTransaction(
		ctx,
		validLoginTransactionCreateInputForTest("http://127.0.0.1:49152/oauth/callback"),
	)
	if !errors.Is(err, ErrLoginTransactionTransport) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want transport and context.Canceled", err)
	}
	assertLoginTransactionErrorRedacted(t, err, "RAW_CONTEXT_TRANSPORT_SECRET")
}
