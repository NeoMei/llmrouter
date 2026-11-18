package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func productOCRPDF(comment string) []byte {
	var file bytes.Buffer
	fmt.Fprintf(&file, "%%PDF-1.7\n%% %s\n", comment)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>",
	}
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = file.Len()
		fmt.Fprintf(&file, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := file.Len()
	fmt.Fprintf(&file, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&file, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&file, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return file.Bytes()
}

func TestProductPaddleOCRScopedDocumentRecovery(t *testing.T) {
	const (
		name   = "PaddleOCR-VL-1.6"
		submit = "/platform/api/v2/ocr/jobs"
		charge = "100000"
	)
	callerKey := "product-ocr-document-1"
	origin := os.Getenv("PRODUCT_CREDIT_INTEGRATION_ORIGIN")
	token, key, proof := "service-service-service-service-token", "uh_key_product_ocr", "product-ocr-proof"
	if origin != "" {
		endpoint, err := url.Parse(origin)
		require.NoError(t, err)
		ip := net.ParseIP(endpoint.Hostname())
		require.True(t, endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || ip != nil && ip.IsLoopback()),
			"integration must use an isolated loopback UniHub")
		callerKey = "product-ocr-document-" + uuid.NewString()
		token = os.Getenv("PRODUCT_CREDIT_INTEGRATION_SERVICE_TOKEN")
		key = os.Getenv("PRODUCT_CREDIT_INTEGRATION_KEY")
		proof = os.Getenv("PRODUCT_CREDIT_INTEGRATION_PROOF")
		require.True(t, token != "" && key != "" && proof != "", "integration requires service token, product key and proof")
	}
	oldFX, oldTariff := operation_setting.USDExchangeRate, billing_setting.ProductCreditPricingJSONString()
	t.Cleanup(func() {
		operation_setting.USDExchangeRate = oldFX
		require.NoError(t, billing_setting.SetProductCreditPricing(oldTariff))
	})
	operation_setting.USDExchangeRate = 1
	require.NoError(t, billing_setting.SetProductCreditPricing(`{
	  "version":"ocr-fixture-1",
	  "suppliers":{"PaddleOCR-VL-1.6":{"ocr":{"paddle":{
	    "version":"paddle-1","currency":"CNY",
	    "tariff":{"version":"metered_v2","operation":"ocr","policy":"success_request",
	      "terms":[{"key":"requests","unit":"request","price":{"numerator":"1","denominator":"1500"}}]}}}}}
	}`))

	var f *ocrGatewayFixture
	var remote platformbilling.RemoteCall
	var final platformbilling.NativeSettleRequest
	conversion := platformbilling.NativeConversion{Version: "product-ocr-fixture", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var output any
		switch {
		case r.URL.Path == "/internal/v1/model-credentials/verify":
			var in map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &in))
			if in["product_authorization"] != proof {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			grant := platformbilling.Claims{
				CreditScope: platformbilling.ProductCreditScope, SourceVerified: true, FundingProductID: "superwagie",
				ID: "ocr-product-key", CredentialKind: "api_key", InstanceID: "default", Audience: "llmrouter", ActorID: "actor", AccountID: "team", OriginProductID: "superwagie",
				ModelScope: "llmrouter_available", PricingSource: platformbilling.NativeCreditPricingSource, CredentialVersion: 2,
				Asset: "CREDIT/6", FundingMode: "credits", NativeCreditConversion: &conversion,
			}
			if in["token"] != key {
				grant.AccountID = "other-team"
			}
			if in["purpose"] == "receipt" {
				grant.NativeCreditConversion = nil
			}
			output = grant
		case r.URL.Path == "/internal/v1/calls/native-reserve":
			f.reserves++
			var in platformbilling.NativeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			require.Equal(t, platformbilling.ProductCreditScope, in.CreditScope)
			require.Equal(t, proof, in.ProductAuthorization)
			require.Equal(t, charge, in.Quote.MaxAmount)
			require.Equal(t, charge, in.MinimumCreditAmount)
			remote = platformbilling.RemoteCall{
				CreditScope: in.CreditScope, CredentialVersion: 2, TaskID: in.TaskID, CallID: in.CallID, CredentialID: "ocr-product-key", InstanceID: "default",
				ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", RequestDigest: in.RequestDigest,
				WireOperation: in.WireOperation, NativePricing: &in.Quote, Rate: platformbilling.Rate{Model: in.Model, Asset: "CREDIT/6", Semantics: "llmrouter_native"},
				Held: in.Quote.MaxAmount, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "superwagie", Status: "RESERVED",
			}
			output = remote
		case strings.HasSuffix(r.URL.Path, "/native-settle"):
			f.settles++
			require.NoError(t, common.DecodeJson(r.Body, &final))
			require.Equal(t, charge, final.NativeQuota)
			require.Equal(t, charge, final.Amount)
			require.NotNil(t, final.Usage)
			require.Equal(t, "provider-owned", final.Usage.Metered.ProviderRequestID)
			remote.Status, remote.Charged = "SETTLED", final.Amount
			output = remote
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/internal/v1/calls/"):
			output = struct {
				platformbilling.RemoteCall
				NativeQuota string                 `json:"native_quota"`
				Usage       *platformbilling.Usage `json:"usage"`
			}{remote, final.NativeQuota, final.Usage}
		default:
			t.Errorf("unexpected product OCR authority endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		data, err := common.Marshal(output)
		require.NoError(t, err)
		_, _ = w.Write(data)
	}))
	defer hub.Close()
	integration := origin != ""
	if !integration {
		origin = hub.URL
	}
	f = newOCRGatewayFixture(t, "success", origin, token, "", "")
	firstPDF := productOCRPDF("first PDF")
	f.productUploads = [][]byte{firstPDF}
	require.NoError(t, f.db.Model(&model.ClientModelRoute{}).Where("id = ?", 1).Update("public_id", name).Error)
	cfg := f.cfg
	cfg.NativeGroup = "platform"
	if integration {
		cfg.InstanceID = os.Getenv("PRODUCT_CREDIT_INTEGRATION_INSTANCE")
	}
	adapter, err := platformbilling.New(f.db, cfg)
	require.NoError(t, err)
	router := gin.New()
	SetManagedKeyDispatch(router, adapter)
	SetPlatformRouter(router, adapter)
	send := func(method, target, credential, authorization string, content []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		var contentType string
		if method == http.MethodPost {
			w := multipart.NewWriter(&body)
			require.NoError(t, w.WriteField("model", name))
			require.NoError(t, w.WriteField("optionalPayload", `{"temperature":0,"useDocOrientationClassify":false}`))
			part, err := w.CreateFormFile("file", "source.pdf")
			require.NoError(t, err)
			_, err = part.Write(content)
			require.NoError(t, err)
			require.NoError(t, w.Close())
			contentType = w.FormDataContentType()
		}
		req := httptest.NewRequest(method, target, &body)
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set(platformbilling.ProductAuthorizationHeader, authorization)
		if method == http.MethodPost {
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("Idempotency-Key", callerKey)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	create := func(pdf []byte) *httptest.ResponseRecorder { return send(http.MethodPost, submit, key, proof, pdf) }
	read := func(suffix string) *httptest.ResponseRecorder {
		return send(http.MethodGet, submit+"/"+callerKey+suffix, key, proof, nil)
	}

	invalid := create([]byte("unconverted Office content"))
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
	require.Zero(t, f.sends)
	require.Zero(t, f.reserves)
	created := create(firstPDF)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	id := created.Header().Get("X-UniHub-Call-ID")
	require.NotEmpty(t, id)
	require.Contains(t, created.Body.String(), `"jobId":"`+id+`"`)
	require.NotContains(t, created.Body.String(), "resultUrl")

	replayed := create(firstPDF)
	require.Equal(t, http.StatusConflict, replayed.Code, replayed.Body.String())
	require.Contains(t, replayed.Body.String(), "logical_call_already_exists")
	conflicting := create(productOCRPDF("different PDF"))
	require.Equal(t, http.StatusConflict, conflicting.Code, conflicting.Body.String())
	require.Contains(t, conflicting.Body.String(), "logical_call_conflict")
	require.Equal(t, 1, f.sends)
	if !integration {
		require.Equal(t, 1, f.reserves)
	}
	require.Equal(t, http.StatusConflict, read("/document").Code)
	pending := read("")
	require.Equal(t, http.StatusOK, pending.Code, pending.Body.String())
	require.Contains(t, pending.Body.String(), `"state":"pending"`)
	finished := read("")
	require.Equal(t, http.StatusOK, finished.Code, finished.Body.String())
	require.Contains(t, finished.Body.String(), `"state":"done"`)
	require.NotContains(t, finished.Body.String(), "resultUrl")
	var grants int64
	require.NoError(t, f.db.Model(&model.OCRResourceGrant{}).Count(&grants).Error)
	require.Zero(t, grants, "checking private result readiness must not create bearer grants")
	owned := read("/document")
	require.Equal(t, http.StatusOK, owned.Code, owned.Body.String())
	require.Equal(t, id, owned.Header().Get("X-UniHub-Call-ID"))
	require.Equal(t, callerKey, owned.Header().Get("X-UniHub-Idempotency-Key"))
	require.Equal(t, "no-store", owned.Header().Get("Cache-Control"))
	sum := sha256.Sum256(owned.Body.Bytes())
	require.Equal(t, hex.EncodeToString(sum[:]), owned.Header().Get("X-Content-SHA256"))
	require.Contains(t, owned.Body.String(), "recognized")
	require.NotContains(t, owned.Body.String(), "paddle.example")
	require.NotContains(t, owned.Body.String(), "image.png")
	ownedAgain := read("/document")
	require.Equal(t, http.StatusOK, ownedAgain.Code, ownedAgain.Body.String())
	require.Equal(t, owned.Body.Bytes(), ownedAgain.Body.Bytes())
	require.Equal(t, 2, f.polls)
	require.Equal(t, http.StatusNotFound, read("/result").Code)
	require.NotEqual(t, http.StatusOK, send(http.MethodGet, submit+"/"+callerKey+"/document", key, "wrong-proof", nil).Code)
	if !integration {
		require.NotEqual(t, http.StatusOK, send(http.MethodGet, submit+"/"+callerKey+"/document", "uh_key_wrong-payer", proof, nil).Code)
		require.Equal(t, 1, f.settles)
	}
	finance := send(http.MethodGet, "/platform/v1/calls/"+callerKey, key, proof, nil)
	require.Equal(t, http.StatusOK, finance.Code, finance.Body.String())
	require.Contains(t, finance.Body.String(), `"charged":"`+charge+`"`)
	require.Equal(t, 1, f.sends)
	if app := os.Getenv("PRODUCT_CREDIT_INTEGRATION_APP_PATH"); integration && app != "" {
		require.True(t, filepath.IsAbs(app), "the SuperWagie fixture must use an absolute checkout")
		secondPDF := productOCRPDF("independent document from SuperWagie")
		f.productUploads = append(f.productUploads, secondPDF)
		pdfPath := filepath.Join(t.TempDir(), "superwagie-ocr-fixture.pdf")
		require.NoError(t, os.WriteFile(pdfPath, secondPDF, 0o600))
		gateway := httptest.NewServer(router)
		t.Cleanup(gateway.Close)
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancel()
		run := exec.CommandContext(ctx, "bun", "run", "--cwd", "packages/web", "test", "server/lib/platform-account/product-ocr-cross-repository.test.js")
		run.Dir = app
		run.Env = append(slices.DeleteFunc(os.Environ(), func(value string) bool {
			return strings.HasPrefix(value, "PRODUCT_CREDIT_INTEGRATION_SERVICE_TOKEN=")
		}), "PRODUCT_CREDIT_INTEGRATION_GATEWAY="+gateway.URL, "PRODUCT_CREDIT_INTEGRATION_OCR_PDF="+pdfPath)
		output, err := run.CombinedOutput()
		secrets := []string{key, "[redacted]", proof, "[redacted]", token, "[redacted]"}
		for _, name := range []string{"PRODUCT_CREDIT_INTEGRATION_ACTOR_TOKEN", "PRODUCT_CREDIT_INTEGRATION_PRODUCT_TOKEN"} {
			if secret := os.Getenv(name); secret != "" {
				secrets = append(secrets, secret, "[redacted]")
			}
		}
		sanitized := strings.NewReplacer(secrets...).Replace(string(output))
		require.NoError(t, err, "SuperWagie product OCR fixture:\n%s", sanitized)
		t.Logf("SuperWagie product OCR fixture:\n%s", sanitized)
		require.Equal(t, 2, f.sends, "each document dispatched exactly once")
		var rows []platformbilling.Call
		require.NoError(t, f.db.Where("credit_scope = ? AND operation = ?", platformbilling.ProductCreditScope, "ocr").Order("call_id").Find(&rows).Error)
		require.Len(t, rows, 2)
		require.NotEqual(t, rows[0].Digest, rows[1].Digest)
		require.NotEqual(t, rows[0].TaskID, rows[1].TaskID)
		require.NotEqual(t, rows[0].ProviderTaskID, rows[1].ProviderTaskID)
		for _, row := range rows {
			require.Equal(t, "SETTLED", row.Status)
			require.Equal(t, charge, row.Charged)
			require.Equal(t, "superwagie", row.FundingProductID)
		}
	}
}
