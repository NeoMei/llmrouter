package helper

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func responsesJSONContext(t *testing.T, path, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	t.Cleanup(func() { common.CleanupBodyStorage(c) })
	return c
}

func requireDuplicateResponsesRequestError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var apiErr *types.NewAPIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
	var duplicateErr *common.DuplicateJSONKeyError
	assert.True(t, errors.As(err, &duplicateErr))
}

func TestResponsesValidationRejectsDuplicateKeysAtEveryDepth(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "model", body: `{"model":"authorized","model":"other","input":[]}`},
		{name: "store", body: `{"model":"model-a","input":[],"store":false,"store":true}`},
		{name: "input", body: `{"model":"model-a","input":[],"input":"replacement"}`},
		{name: "previous response", body: `{"model":"model-a","input":[],"previous_response_id":"one","previous_response_id":"two"}`},
		{name: "nested client metadata", body: `{"model":"model-a","input":[],"client_metadata":{"task_id":"one","task_id":"two"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := GetAndValidateResponsesRequest(responsesJSONContext(t, "/v1/responses", test.body))
			requireDuplicateResponsesRequestError(t, err)
		})
	}
}

func TestResponsesCompactValidationRejectsDuplicateKeysAtEveryDepth(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "model", body: `{"model":"authorized","model":"other","input":[]}`},
		{name: "nested unknown extension", body: `{"model":"model-a","input":[],"vendor":{"mode":"one","mode":"two"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := GetAndValidateResponsesCompactionRequest(responsesJSONContext(t, "/v1/responses/compact", test.body))
			requireDuplicateResponsesRequestError(t, err)
		})
	}
}

func TestResponsesUniqueValidationPreservesLegalRawBodyForReplay(t *testing.T) {
	raw := `{"model":"model-a","input":[],"background":true,"vendor":{"large":9007199254740993}}`
	c := responsesJSONContext(t, "/v1/responses", raw)
	request, err := GetAndValidateResponsesRequest(c)
	require.NoError(t, err)
	assert.Equal(t, "model-a", request.Model)

	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	reader, err := storage.NewReader()
	require.NoError(t, err)
	defer reader.Close()
	replayed, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, raw, string(replayed))
}
