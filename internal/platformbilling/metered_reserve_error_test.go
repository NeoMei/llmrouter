package platformbilling

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type meteredReserveTransport func(*http.Request) (*http.Response, error)

func (f meteredReserveTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestMeteredReserveRejectionAndUncertaintyNeverRedispatch(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body          string
		transportErr  error
		wantStatus    int
		wantCallState string
	}{
		{name: "insufficient-funds", status: 402, body: `{"error":"insufficient_funds"}`, wantStatus: 402, wantCallState: "REJECTED"},
		{name: "insufficient-credits", status: 402, body: `{"error":"insufficient_credits"}`, wantStatus: 402, wantCallState: "REJECTED"},
		{name: "ambiguous-payment-required", status: 402, wantStatus: 503, wantCallState: "RESERVE_UNKNOWN"},
		{name: "forbidden", status: 403, body: `{"error":"insufficient_funds"}`, wantStatus: 403, wantCallState: "RESERVE_UNKNOWN"},
		{name: "conflict", status: 409, wantStatus: 409, wantCallState: "RESERVE_UNKNOWN"},
		{name: "service-unavailable", status: 503, wantStatus: 503, wantCallState: "RESERVE_UNKNOWN"},
		{name: "connection-interrupted", transportErr: io.ErrUnexpectedEOF, wantStatus: 503, wantCallState: "RESERVE_UNKNOWN"},
		{name: "timeout", transportErr: context.DeadlineExceeded, wantStatus: 503, wantCallState: "RESERVE_UNKNOWN"},
		{name: "wrong-success-receipt", status: 200, body: `{"call_id":"different-call","status":"RESERVED"}`, wantStatus: 503, wantCallState: "RESERVE_UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, db, settles := meteredFixture(t, "speech")
			reserves, lookups, releases := 0, 0, 0
			a.client.http.Transport = meteredReserveTransport(func(r *http.Request) (*http.Response, error) {
				status, body := 0, ""
				switch r.URL.Path {
				case "/internal/v1/calls/reserve":
					reserves++
					if tc.transportErr != nil {
						return nil, tc.transportErr
					}
					status, body = tc.status, tc.body
				case "/internal/v1/calls/call":
					lookups++
					status, body = http.StatusNotFound, `{}`
				case "/internal/v1/calls/call/release":
					releases++
				}
				if status != 0 {
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				}
				return http.DefaultTransport.RoundTrip(r)
			})
			ctx := context.Background()
			body := []byte(`{"model":"test","input":"hello","voice":"alloy"}`)
			session, err := a.AdmitOperation(ctx, "uh_model_token", "task", "call", "speech", "application/json", body)
			require.Error(t, err)
			assert.Nil(t, session, "failed reservation must not authorize provider dispatch")
			assert.Equal(t, tc.wantStatus, HTTPStatus(err))
			var row Call
			require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
			assert.Equal(t, tc.wantCallState, row.Status)

			session, err = a.AdmitOperation(ctx, "uh_model_token", "task", "call", "speech", "application/json", body)
			assert.Nil(t, session)
			assert.ErrorIs(t, err, ErrDuplicate)
			require.NoError(t, a.Recover(ctx))
			require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
			assert.Equal(t, tc.wantCallState, row.Status)
			assert.Equal(t, 1, reserves, "neither duplicate admission nor recovery may reserve again")
			if tc.wantCallState == "REJECTED" {
				assert.Zero(t, lookups, "definitive rejection needs no uncertain-hold recovery")
			} else {
				assert.Equal(t, 1, lookups)
			}
			assert.Zero(t, releases)
			assert.Zero(t, settles.Load())
			var attempts int64
			require.NoError(t, db.Model(&Attempt{}).Count(&attempts).Error)
			assert.Zero(t, attempts)
		})
	}
}

func TestLegacyReserve402RequiresConfirmedRejection(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantCallState string
		wantStatus                int
	}{
		{name: "confirmed", body: `{"error":"insufficient_funds"}`, wantCallState: "REJECTED", wantStatus: 402},
		{name: "ambiguous", body: `{}`, wantCallState: "RESERVE_UNKNOWN", wantStatus: 503},
		{name: "native-budget-code", body: `{"error":"usage_budget_exceeded"}`, wantCallState: "RESERVE_UNKNOWN", wantStatus: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failSettle := false
			a, db := fixture(t, &failSettle)
			reserves := 0
			a.client.http.Transport = meteredReserveTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/internal/v1/calls/reserve" {
					reserves++
					return &http.Response{StatusCode: 402, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
				}
				return http.DefaultTransport.RoundTrip(r)
			})
			body := []byte(`{"model":"test","messages":[{"role":"user","content":"hello"}],"max_tokens":50}`)
			session, err := a.Admit(context.Background(), "uh_model_token", "task", "call", body)
			require.Error(t, err)
			assert.Nil(t, session)
			assert.Equal(t, tc.wantStatus, HTTPStatus(err))
			var row Call
			require.NoError(t, db.First(&row, "call_id = ?", "call").Error)
			assert.Equal(t, tc.wantCallState, row.Status)
			session, err = a.Admit(context.Background(), "uh_model_token", "task", "call", body)
			assert.Nil(t, session)
			assert.ErrorIs(t, err, ErrDuplicate)
			assert.Equal(t, 1, reserves)
		})
	}
}

func TestNonReserve402DoesNotClaimInsufficientFunds(t *testing.T) {
	failSettle := false
	a, _ := fixture(t, &failSettle)
	a.client.http.Transport = meteredReserveTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 402, Body: io.NopCloser(strings.NewReader(`{"error":"insufficient_funds"}`)), Request: r}, nil
	})
	err := a.client.do(context.Background(), http.MethodPost, "/internal/v1/model-credentials/verify", "", nil, nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, HTTPStatus(err), "only reserve can certify a rejected hold")
}
