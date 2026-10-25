package helper

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/capability"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDeclaredModelAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, metadata, body string
		invalid              bool
	}{
		{"legacy video", `{}`, `{"messages":[{"content":[{"type":"video_url","video_url":{"url":"https://example.test/v.mp4","fps":2}}]}]}`, false},
		{"explicit text rejects image", `{"input_modalities":["text"]}`, `{"messages":[{"content":[{"type":"image_url","image_url":{"url":"https://example.test/i.png"}}]}]}`, true},
		{"video separate from audio", `{"input_modalities":["text","video"]}`, `{"messages":[{"content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]}`, true},
		{"video object detected", `{"input_modalities":["text"]}`, `{"messages":[{"content":[{"type":"video_url","video_url":{"url":"https://example.test/v.mp4","fps":2}}]}]}`, true},
		{"wrong operation", `{"operations":["embeddings.create"]}`, `{"messages":[]}`, true},
		{"stream explicitly disabled", `{"streaming":false}`, `{"stream":true,"messages":[]}`, true},
		{"image count", `{"media_limits":{"max_images":1}}`, `{"messages":[{"content":[{"type":"image_url"},{"type":"image_url"}]}]}`, true},
		{"unverified image pixels", `{"media_limits":{"max_pixels":100}}`, `{"messages":[{"content":[{"type":"image_url","image_url":{"url":"https://example.test/i.png"}}]}]}`, true},
		{"bounded request", `{"media_limits":{"max_request_bytes":1}}`, `{"messages":[]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var contract dto.ModelCapabilities
			require.NoError(t, common.Unmarshal([]byte(tc.metadata), &contract))
			var req dto.GeneralOpenAIRequest
			require.NoError(t, common.Unmarshal([]byte(tc.body), &req))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
			info := &relaycommon.RelayInfo{Request: &req, IsStream: req.IsStream(c)}
			err := ValidateModelAdmission(c, info, contract, capability.OperationChatCreate)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEmbeddingRequestDimensionsAndShapes(t *testing.T) {
	for _, tc := range []struct {
		body    string
		invalid bool
	}{
		{`{"model":"text-embedding-v4","input":["a","b"],"dimensions":1024}`, false},
		{`{"input":[1,2,3]}`, false}, {`{"input":[[1,2],[3,4]]}`, false},
		{`{"input":["a",2]}`, true}, {`{"input":[],"dimensions":1024}`, true},
		{`{"input":"a","dimensions":0}`, true}, {`{"input":"a","dimensions":-1}`, true}, {`{"input":"a","dimensions":65537}`, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			req, err := GetAndValidateEmbeddingRequest(c, relayconstant.RelayModeEmbeddings)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, req)
			}
		})
	}
}

func TestDeclaredModelOutputAdmission(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{}`))
	req := &dto.EmbeddingRequest{Input: "hi"}
	info := &relaycommon.RelayInfo{Request: req}
	require.Error(t, ValidateModelAdmission(c, info, dto.ModelCapabilities{OutputModalities: []string{"text"}}, capability.OperationEmbeddingCreate))
}

func TestFixedEmbeddingContractRejectsOtherResponsePaths(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	info := &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{}}
	contract := dto.ModelCapabilities{EmbeddingSpace: &dto.EmbeddingSpace{Provider: "aliyun", Model: "text-embedding-v4", Version: "v4", Dimensions: 1024, InputSemantics: "text"}}
	require.Error(t, ValidateModelAdmission(c, info, contract, capability.OperationChatCreate))
}
