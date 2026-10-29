package ali

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOaiFormEdit2WanxImageEditPreservesMaskInputs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("prompt", "replace the object"))
	image := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	mask := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	part, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = part.Write(image)
	require.NoError(t, err)
	part, err = writer.CreateFormFile("mask", "mask.png")
	require.NoError(t, err)
	_, err = part.Write(mask)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest("POST", "/v1/images/edits", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req
	converted, err := oaiFormEdit2WanxImageEdit(ctx, &relaycommon.RelayInfo{}, dto.ImageRequest{Model: "wanx2.1-imageedit", Prompt: "replace the object"})
	require.NoError(t, err)
	input, ok := converted.Input.(WanImageInput)
	require.True(t, ok)
	require.Equal(t, "description_edit_with_mask", input.Function)
	require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(image), input.BaseImageURL)
	require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(mask), input.MaskImageURL)
}
