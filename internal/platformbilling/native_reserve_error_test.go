package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeReserveBudgetRejectionRequiresExactNoHoldContract(t *testing.T) {
	for _, tc := range []struct {
		name, body, state string
		status, wantHTTP  int
	}{
		{"budget", `{"error":"usage_budget_exceeded"}`, "REJECTED", 402, 402},
		{"funds", `{"error":"insufficient_funds"}`, "REJECTED", 402, 402},
		{"forbidden", `{"error":"usage_budget_exceeded"}`, "RESERVE_UNKNOWN", 403, 403},
		{"ambiguous", `{"error":"usage_budget_exceeded","hold":"unknown"}`, "RESERVE_UNKNOWN", 402, 503},
		{"duplicate-error", `{"error":"usage_budget_exceeded","error":"insufficient_funds"}`, "RESERVE_UNKNOWN", 402, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failSettle := false
			a, db := fixture(t, &failSettle)
			a.config.NativeGroup = "default"
			reserves, lookups := 0, 0
			a.client.http.Transport = meteredReserveTransport(func(r *http.Request) (*http.Response, error) {
				status, body := http.StatusNotFound, `{}`
				switch r.URL.Path {
				case "/internal/v1/calls/native-reserve":
					reserves++
					status, body = tc.status, tc.body
				case "/internal/v1/calls/native-budget":
					lookups++
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			row := Call{CallID: "native-budget", CredentialID: "same-key", CredentialVersion: 2, ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", InstanceID: "default", TaskID: "task", Model: "model", Operation: "chat", WireOperation: "chat.completions.create", PricingSource: "llmrouter_native", Digest: "request", Asset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Status: "ADMITTED"}
			require.NoError(t, db.Create(&row).Error)
			s := &Session{adapter: a, call: row, nativeCredential: "uh_key_existing"}
			snapshot := `{"native_price":"frozen"}`
			sum := sha256.Sum256([]byte(snapshot))
			quote := NativeQuote{PricingVersion: "lr-native-v1", PricingDigest: hex.EncodeToString(sum[:]), BillingModel: "model", WireOperation: row.WireOperation, RouteID: "0", ChannelID: "1", Group: "default", NativeQuotaMax: "10", Conversion: NativeConversion{Version: "lr-quota-usd-cny-v1", Numerator: "5", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}, Asset: "CNY/6", MaxAmount: "25"}
			err := s.ReserveNative(context.Background(), quote, snapshot)
			require.Error(t, err)
			assert.Equal(t, tc.wantHTTP, HTTPStatus(err))
			require.NoError(t, db.First(&row, "call_id = ?", row.CallID).Error)
			assert.Equal(t, tc.state, row.Status)
			require.Error(t, s.ReserveNative(context.Background(), quote, snapshot))
			require.NoError(t, a.Recover(context.Background()))
			require.NoError(t, db.First(&row, "call_id = ?", row.CallID).Error)
			assert.Equal(t, tc.state, row.Status, "a later 404 is not proof of no historical hold")
			assert.Equal(t, 1, reserves, "uncertainty and duplicate calls must not replay reservation")
			if tc.state == "REJECTED" {
				assert.Zero(t, lookups)
			} else {
				assert.Equal(t, 1, lookups)
			}
			var attempts int64
			require.NoError(t, db.Model(&Attempt{}).Count(&attempts).Error)
			assert.Zero(t, attempts, "failed admission cannot dispatch to a provider")
		})
	}
}
