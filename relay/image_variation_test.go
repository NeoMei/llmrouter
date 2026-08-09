package relay_test

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageVariationMultipartContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	require.NoError(t, writer.WriteField("model", "dall-e-2"))
	require.NoError(t, writer.WriteField("n", "2"))
	require.NoError(t, writer.WriteField("size", "512x512"))
	require.NoError(t, writer.WriteField("response_format", "b64_json"))
	imagePart, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = imagePart.Write(minimalPNG())
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, "/v1/images/variations", &requestBody)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	parsed, err := helper.GetAndValidOpenAIImageRequest(context, relayconstant.RelayModeImagesVariations)
	require.NoError(t, err)
	require.NotNil(t, parsed.N)
	assert.Equal(t, uint(2), *parsed.N)
	assert.Equal(t, "b64_json", parsed.ResponseFormat)

	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeImagesVariations, RequestURLPath: "/v1/images/variations",
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://api.openai.com", ChannelType: constant.ChannelTypeOpenAI},
	}
	adaptor := &openai.Adaptor{}
	converted, err := adaptor.ConvertImageRequest(context, info, *parsed)
	require.NoError(t, err)
	body, ok := converted.(*bytes.Buffer)
	require.True(t, ok)
	_, params, err := mime.ParseMediaType(context.Request.Header.Get("Content-Type"))
	require.NoError(t, err)
	form, err := multipart.NewReader(bytes.NewReader(body.Bytes()), params["boundary"]).ReadForm(1 << 20)
	require.NoError(t, err)
	defer form.RemoveAll()
	assert.Equal(t, "2", form.Value["n"][0])
	assert.Equal(t, "512x512", form.Value["size"][0])
	require.Len(t, form.File["image"], 1)
	file, err := form.File["image"][0].Open()
	require.NoError(t, err)
	forwarded, err := io.ReadAll(file)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	assert.Equal(t, minimalPNG(), forwarded)
	requestURL, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	parsedURL, err := url.Parse(requestURL)
	require.NoError(t, err)
	assert.Equal(t, "/v1/images/variations", parsedURL.Path)
}

func TestImageVariationRejectsInvalidCountAndMIMEConflict(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		filename string
		content  []byte
		n        string
	}{
		{name: "zero count", filename: "source.png", content: minimalPNG(), n: "0"},
		{name: "mime conflict", filename: "source.jpg", content: minimalPNG(), n: "1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			require.NoError(t, writer.WriteField("n", testCase.n))
			part, err := writer.CreateFormFile("image", testCase.filename)
			require.NoError(t, err)
			_, err = part.Write(testCase.content)
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			request := httptest.NewRequest(http.MethodPost, "/v1/images/variations", &body)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = request
			_, err = helper.GetAndValidOpenAIImageRequest(context, relayconstant.RelayModeImagesVariations)
			require.Error(t, err)
		})
	}
}

func TestImageVariationPathHasDedicatedRelayMode(t *testing.T) {
	assert.Equal(t, relayconstant.RelayModeImagesVariations, relayconstant.Path2RelayMode("/v1/images/variations"))
}

func minimalPNG() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
}
