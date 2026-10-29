package router

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/ocr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWanResultRejectsUnconfirmedAuthority(t *testing.T) {
	f := newWanVideoGatewayFixture(t, "720")
	require.Equal(t, http.StatusOK, f.submit(t, "720P").Code)
	oldLimit := constant.TaskQueryLimit
	constant.TaskQueryLimit = 10
	t.Cleanup(func() { constant.TaskQueryLimit = oldLimit })
	_ = service.RunTaskPollingOnce(context.Background(), nil)
	f.denySettlementRead = true
	result := f.read(t, "/result", "uh_read_"+strings.Repeat("b", 64))
	assert.Equal(t, http.StatusConflict, result.Code)
	assert.NotEqual(t, f.mp4, result.Body.Bytes())
	f.denySettlementRead = false
	result = f.read(t, "/result", "uh_read_"+strings.Repeat("b", 64))
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	assert.Equal(t, f.mp4, result.Body.Bytes())
	assert.Equal(t, 1, f.settles)
}

func TestOCROutputGrantRejectsUnconfirmedAuthority(t *testing.T) {
	f := newOCRGatewayFixture(t, "success", "", "", "", "")
	ct, body := ocrMultipart(t, f.image)
	token, read := "uh_model_"+strings.Repeat("a", 64), "uh_read_"+strings.Repeat("b", 64)
	require.Equal(t, http.StatusOK, f.request(t, "POST", "/platform/api/v2/ocr/jobs", token, ct, body).Code)
	status := f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil)
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	status = f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil)
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	var out struct {
		Data ocr.JobStatus `json:"data"`
	}
	require.NoError(t, common.Unmarshal(status.Body.Bytes(), &out))
	u, err := url.Parse(out.Data.ResultURL.JSONURL)
	require.NoError(t, err)
	require.NotEmpty(t, u.RawQuery)
	f.denySettlementRead = true
	deniedStatus := f.request(t, "GET", "/platform/api/v2/ocr/jobs/ocr-call", read, "", nil)
	assert.NotEqual(t, http.StatusOK, deniedStatus.Code)
	denied := f.request(t, "GET", u.RequestURI(), "", "", nil)
	assert.NotEqual(t, http.StatusOK, denied.Code)
	assert.NotContains(t, denied.Body.String(), "recognized")
	f.denySettlementRead = false
	result := f.request(t, "GET", u.RequestURI(), "", "", nil)
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	assert.Contains(t, result.Body.String(), "recognized")
	assert.Equal(t, 1, f.settles)
}
