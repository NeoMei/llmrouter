package router

import (
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductPinnedKnowledgeRoutesUseRealUniHub(t *testing.T) {
	origin := os.Getenv("PRODUCT_CREDIT_INTEGRATION_ORIGIN")
	if origin == "" {
		t.Skip("isolated UniHub product-credit HTTP fixture is not configured")
	}
	endpoint, err := url.Parse(origin)
	require.NoError(t, err)
	ip := net.ParseIP(endpoint.Hostname())
	require.True(t, endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || ip != nil && ip.IsLoopback()),
		"integration must use an isolated loopback UniHub")
	proof := os.Getenv("PRODUCT_CREDIT_INTEGRATION_PROOF")
	key := os.Getenv("PRODUCT_CREDIT_INTEGRATION_KEY")
	token := os.Getenv("PRODUCT_CREDIT_INTEGRATION_SERVICE_TOKEN")
	actorToken := os.Getenv("PRODUCT_CREDIT_INTEGRATION_ACTOR_TOKEN")
	require.True(t, proof != "" && platformbilling.IsModelCredential(key) && len(token) >= 32 && actorToken != "", "integration authority is required")
	account := os.Getenv("PRODUCT_CREDIT_INTEGRATION_ACCOUNT")
	require.NotEmpty(t, account)

	actorReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint.JoinPath("v1", "me", "product-access").String(), nil)
	require.NoError(t, err)
	actorReq.Header.Set("Authorization", "Bearer "+actorToken)
	center := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := center.Do(actorReq)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var actor struct {
		Issuer   string `json:"issuer"`
		Subject  string `json:"subject"`
		Product  string `json:"product_id"`
		Instance string `json:"instance_id"`
	}
	require.NoError(t, common.DecodeJson(response.Body, &actor))
	require.NotEmpty(t, actor.Issuer)
	require.Equal(t, "superwagie", actor.Product)

	f := newProductKnowledgeFixture(t, true)
	f.managed.credential = key
	f.config.Origin, f.config.ServiceToken, f.config.IdentityIssuer = origin, token, actor.Issuer
	f.config.InstanceID = actor.Instance
	adapter, err := platformbilling.New(f.db, f.config)
	require.NoError(t, err)
	f.adapter = adapter
	f.engine = gin.New()
	SetManagedKeyDispatch(f.engine, f.adapter)
	bindings, err := adapter.ActivateManagedKnowledge(t.Context(), f.name, f.wire, []string{actor.Product})
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	f.binding = bindings[0]

	call := "wk_" + strings.Repeat("0", 28) + strings.ReplaceAll(uuid.NewString(), "-", "")
	f.taskID = "knowledge-integration-" + uuid.NewString()
	send := func(path, body, callerKey, pin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actorToken)
		r.Header.Set("X-UniHub-Task-ID", f.taskID)
		r.Header.Set(platformbilling.ProductAuthorizationHeader, proof)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if callerKey != "" {
			r.Header.Set("Idempotency-Key", callerKey)
		}
		if pin != "" {
			r.Header.Set("X-UniHub-Knowledge-Policy", pin)
		}
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, r)
		return w
	}
	meta := send("/platform/v1/knowledge/route", `{"model":"text-embedding-v4","wire_operation":"embeddings.create"}`, "", "")
	require.Equal(t, http.StatusOK, meta.Code, meta.Body.String())
	var route platformbilling.KnowledgePaidRoute
	require.NoError(t, common.UnmarshalUnique(meta.Body.Bytes(), &route))
	require.Equal(t, f.binding.PolicyDigest, route.PolicyDigest)
	require.Equal(t, account, route.AccountID)
	require.Equal(t, actor.Subject, route.ActorID)
	require.Equal(t, actor.Instance, route.InstanceID)

	grant := f.productGrant()
	grant.InstanceID, grant.ActorID, grant.AccountID, grant.ID = actor.Instance, route.ActorID, route.AccountID, route.AuthorizedCredentialID
	scope, err := platformbilling.WithProductAuthorization(t.Context(), proof, call)
	require.NoError(t, err)
	f.callID, err = grant.ProductCreditCallID(scope)
	require.NoError(t, err)

	sent := send("/platform/v1/embeddings", f.body, call, route.PolicyDigest)
	require.Equal(t, http.StatusOK, sent.Code, sent.Body.String())
	assert.Equal(t, 1, f.sends)
	var row platformbilling.Call
	require.NoError(t, f.db.First(&row, "call_id = ?", f.callID).Error)
	assert.Equal(t, "SETTLED", row.Status)
	assert.Equal(t, "3150", row.Charged)
	assert.Equal(t, actor.Product, row.FundingProductID)
	assert.Equal(t, route.AuthorizedCredentialID, row.CredentialID)
	recovered := send("/platform/v1/knowledge/results/"+call, "", "", "")
	require.Equal(t, http.StatusOK, recovered.Code, recovered.Body.String())
	assert.Equal(t, sent.Body.String(), recovered.Body.String())
	encoded, err := base64.RawURLEncoding.DecodeString(recovered.Header().Get("X-UniHub-Knowledge-Receipt"))
	require.NoError(t, err)
	var receipt platformbilling.KnowledgeReceipt
	require.NoError(t, common.UnmarshalUnique(encoded, &receipt))
	assert.Equal(t, f.taskID, receipt.TaskID)
	assert.Equal(t, call, receipt.CallID)
	assert.Equal(t, row.CredentialID, receipt.AuthorizedCredentialID)
	assert.Equal(t, row.CredentialID, receipt.ProducerCredentialID)
	assert.Equal(t, http.StatusConflict, send("/platform/v1/embeddings", f.body, call, route.PolicyDigest).Code)
	assert.Equal(t, 1, f.sends)
}
