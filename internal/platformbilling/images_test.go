package platformbilling

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"image"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/textproto"
	"testing"
)

func imagePolicyFixture(wire string) ImagePolicy {
	p := ImagePolicy{Version: "image_v1", PublicModel: "image-model", WireOperation: wire, ChannelID: 1, ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "gpt-image-1", ChannelKeySHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Origin: "https://api.openai.com", APIVersion: "v1", MaxBodyBytes: 10 << 20, MaxImageBytes: 1 << 20, MaxPixels: 16 << 20, MaxImages: 3, MaxInputImages: 3, RateIDs: map[string]string{"CNY/6": "image-rate"}}
	if wire == "gemini.images.generate" {
		p.ChannelType = constant.ChannelTypeGemini
		p.UpstreamModel = "gemini-image-certified"
		p.Origin = "https://generativelanguage.googleapis.com"
		p.APIVersion = "v1beta"
	}
	return p
}
func imageFixtureBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	return b.Bytes()
}

func TestWanMaskedEditPolicyAcceptsCertifiedMultipart(t *testing.T) {
	p := imagePolicyFixture("images.edits.create")
	p.PublicModel = "wanx2.1-imageedit"
	p.UpstreamModel = "wanx2.1-imageedit"
	p.ChannelType = constant.ChannelTypeAli
	p.Origin = "https://dashscope.aliyuncs.com"
	p.MaxInputImages = 1
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", p.PublicModel))
	require.NoError(t, writer.WriteField("prompt", "replace the object"))
	for _, name := range []string{"image", "mask"} {
		header := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="` + name + `"; filename="` + name + `.png"`}, "Content-Type": {"image/png"}}
		part, err := writer.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write(imageFixtureBytes(t))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	parsed, err := parseImageOperation(p, WireOperation{ID: p.WireOperation}, writer.FormDataContentType(), body.Bytes())
	require.NoError(t, err)
	assert.Equal(t, 1, parsed.Quantity)
}
func TestImageNativePolicyAdmission(t *testing.T) {
	p := imagePolicyFixture("gemini.images.generate")
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"slide"}]}],"generationConfig":{"responseModalities":["IMAGE"],"responseFormat":{"image":{"aspectRatio":"ASPECT_RATIO_SIXTEEN_BY_NINE","imageSize":"IMAGE_SIZE_TWO_K","mimeType":"IMAGE_JPEG"}},"temperature":0}}`)
	r, e := parseImageOperation(p, WireOperation{ID: p.WireOperation, PathModel: p.PublicModel, Mode: "generateContent"}, "application/json", body)
	require.NoError(t, e)
	assert.Equal(t, 3, r.Quantity)
	assert.Contains(t, string(r.Canonical), `"temperature":0`)
	_, e = parseGeminiOperation(Config{MaxInputTokens: 10000, MaxOutputTokens: 1000}, p.PublicModel, body)
	require.Error(t, e)
	for _, bad := range []string{`"IMAGE"`, `"IMAGE","TEXT"`} {
		modified := bytes.Replace(body, []byte(`"IMAGE"`), []byte(bad), 1)
		if bad == `"IMAGE"` {
			modified = bytes.Replace(body, []byte(`IMAGE_SIZE_TWO_K`), []byte(`UNKNOWN_SIZE`), 1)
		}
		_, e = parseImageOperation(p, WireOperation{ID: p.WireOperation, PathModel: p.PublicModel, Mode: "generateContent"}, "application/json", modified)
		require.Error(t, e)
	}
}
func TestImageActualOutputIsCompleteUniqueAndCounted(t *testing.T) {
	p := imagePolicyFixture("images.generations.create")
	encoded := base64.StdEncoding.EncodeToString(imageFixtureBytes(t))
	raw := []byte(`{"data":[{"b64_json":"` + encoded + `"}],"usage":{"output_tokens":0}}`)
	result, e := ObserveImageResult(p, raw, nil)
	require.NoError(t, e)
	require.Len(t, result.Outputs, 1)
	assert.Equal(t, 2, result.Outputs[0].Width)
	assert.Contains(t, string(result.Usage), `"output_tokens":0`)
	for _, bad := range []string{`{"data":[]}`, `{"data":[{"b64_json":"` + encoded + `"},{"b64_json":"` + encoded + `"}]}`, `{"data":[{"b64_json":"aW52YWxpZA=="}]}`, `{"data":[{"b64_json":"` + encoded + `"}],"usage":{"image_count":2}}`} {
		_, e := ObserveImageResult(p, []byte(bad), nil)
		require.Error(t, e)
	}
	var out map[string]any
	require.NoError(t, common.Unmarshal(result.Body, &out))
	assert.NotNil(t, out["data"])
}

func TestImageRejectsInventedNativeEnumsAndUnboundedDimensions(t *testing.T) {
	p := imagePolicyFixture("gemini.images.generate")
	for _, format := range []string{`{"aspectRatio":"ASPECT_RATIO_ONE_TO_ONE"}`, `{"mimeType":"IMAGE_PNG"}`} {
		body := []byte(`{"contents":[{"parts":[{"text":"slide"}]}],"generationConfig":{"responseModalities":["IMAGE"],"responseFormat":{"image":` + format + `}}}`)
		_, err := parseImageOperation(p, WireOperation{ID: p.WireOperation, PathModel: p.PublicModel, Mode: "generateContent"}, "application/json", body)
		require.Error(t, err)
	}
	p = imagePolicyFixture("images.generations.create")
	for _, size := range []string{"99999999x99999999", "0x1", "wrong"} {
		_, err := parseImageOperation(p, WireOperation{ID: p.WireOperation}, "application/json", []byte(`{"model":"image-model","prompt":"slide","size":"`+size+`"}`))
		require.Error(t, err)
	}
}

func TestImageOutputRejectsUntrustedExtraResourcesAndUsage(t *testing.T) {
	p := imagePolicyFixture("images.generations.create")
	b64 := base64.StdEncoding.EncodeToString(imageFixtureBytes(t))
	for _, suffix := range []string{`,"debug_url":"https://supplier.example/private"`, `,"usage":{"secret":"supplier-key"}`, `,"usage":{"image_count":-1}`, `,"usage":{"image_count":1,"image_count":2}`} {
		_, e := ObserveImageResult(p, []byte(`{"data":[{"b64_json":"`+b64+`"}]`+suffix+`}`), nil)
		require.Error(t, e)
	}
}

func TestImageRejectsConcatenatedContainers(t *testing.T) {
	p := imagePolicyFixture("images.generations.create")
	raw := imageFixtureBytes(t)
	_, e := imageBytes(p, append(append([]byte{}, raw...), raw...), "image/png")
	require.Error(t, e)
}

func TestImageJPEGFullContainer(t *testing.T) {
	p := imagePolicyFixture("images.generations.create")
	var b bytes.Buffer
	require.NoError(t, jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil))
	_, e := imageBytes(p, b.Bytes(), "image/jpeg")
	require.NoError(t, e)
	joined := append(append([]byte{}, b.Bytes()...), b.Bytes()...)
	_, e = imageBytes(p, joined, "image/jpeg")
	require.Error(t, e)
}

func TestImageWebPBoundedFullDecode(t *testing.T) {
	p := imagePolicyFixture("images.generations.create")
	raw, e := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	require.NoError(t, e)
	fact, e := imageBytes(p, raw, "image/webp")
	require.NoError(t, e)
	assert.Equal(t, 1, fact.Width)
	assert.Equal(t, 1, fact.Height)
	_, e = imageBytes(p, raw[:len(raw)-1], "image/webp")
	require.Error(t, e)
}

func TestImageGeminiSafetyBlockedIsNotProduction(t *testing.T) {
	p := imagePolicyFixture("gemini.images.generate")
	b64 := base64.StdEncoding.EncodeToString(imageFixtureBytes(t))
	raw := []byte(`{"candidates":[{"finishReason":"STOP","safetyRatings":[{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","probability":"HIGH","blocked":true}],"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"` + b64 + `"}}]}}]}`)
	_, e := ObserveImageResult(p, raw, nil)
	require.Error(t, e)
}

func TestImageGeminiNativeUsageSchema(t *testing.T) {
	p := imagePolicyFixture("gemini.images.generate")
	b64 := base64.StdEncoding.EncodeToString(imageFixtureBytes(t))
	for _, tc := range []struct {
		name, usage string
		valid       bool
	}{
		{"native", `{"promptTokenCount":258,"candidatesTokenCount":1290,"totalTokenCount":1548,"promptTokensDetails":[{"modality":"IMAGE","tokenCount":258}],"cacheTokensDetails":[{"modality":"TEXT","tokenCount":0}],"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1290}],"toolUsePromptTokensDetails":[],"serviceTier":"standard"}`, true},
		{"flex", `{"serviceTier":"flex"}`, true}, {"priority", `{"serviceTier":"priority"}`, true}, {"unspecified", `{"serviceTier":"unspecified"}`, true},
		{"invalid-tier", `{"serviceTier":"https://supplier.example"}`, false},
		{"invalid-modality", `{"promptTokensDetails":[{"modality":"secret","tokenCount":0}]}`, false},
		{"detail-secret", `{"promptTokensDetails":[{"modality":"IMAGE","tokenCount":0,"secret":"key"}]}`, false},
		{"fraction", `{"candidatesTokenCount":0.5}`, false}, {"negative", `{"candidatesTokenCount":-1}`, false},
		{"nested-number", `{"candidatesTokenCount":{"value":1}}`, false}, {"unknown-number", `{"secret":1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"` + b64 + `"}}]}}],"usageMetadata":` + tc.usage + `}`)
			result, err := ObserveImageResult(p, raw, nil)
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.usage, string(result.Usage))
			assert.Len(t, result.Outputs, 1)
		})
	}
}

func TestImageMetadataNativeScalarsAndSafeProjection(t *testing.T) {
	p := imagePolicyFixture("images.generations.create")
	b64 := base64.StdEncoding.EncodeToString(imageFixtureBytes(t))
	for _, tc := range []struct {
		name, suffix string
		valid        bool
	}{
		{"native", `,"created":1,"size":"2x2","quality":"high","background":"opaque","output_format":"png"`, true},
		{"created-object", `,"created":{"url":"https://supplier.example/private"}`, false},
		{"size-object", `,"size":{"url":"https://supplier.example/private"}`, false},
		{"quality-object", `,"quality":{"secret":"key"}`, false},
		{"bad-format", `,"output_format":"https://supplier.example/private"`, false},
		{"fraction-created", `,"created":1.5`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ObserveImageResult(p, []byte(`{"data":[{"b64_json":"`+b64+`"}]`+tc.suffix+`}`), nil)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	for _, prompt := range []string{"A scene: blue sky, 16:9. 中文", "An image at https://supplier.example/private?signature=synthetic"} {
		raw, err := common.Marshal(map[string]any{"data": []any{map[string]any{"b64_json": b64, "revised_prompt": prompt}}})
		require.NoError(t, err)
		result, err := ObserveImageResult(p, raw, nil)
		require.NoError(t, err)
		if prompt[0:7] == "A scene" {
			assert.Contains(t, string(result.Body), prompt)
		} else {
			assert.NotContains(t, string(result.Body), "supplier.example")
			assert.NotContains(t, string(result.Body), "revised_prompt")
		}
	}
}

func TestImageWebPFullChunkSequence(t *testing.T) {
	p := imagePolicyFixture("images.generations.create")
	raw, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		tail  []byte
		valid bool
	}{
		{"duplicate", raw[12:], false},
		{"truncated", []byte{'J', 'U', 'N', 'K', 255, 255, 255, 127}, false},
		{"partial-header", []byte{'J'}, false},
		{"missing-pad", []byte{'J', 'U', 'N', 'K', 1, 0, 0, 0, 42}, false},
		{"nonzero-pad", []byte{'J', 'U', 'N', 'K', 1, 0, 0, 0, 42, 1}, false},
		{"valid-pad", []byte{'J', 'U', 'N', 'K', 1, 0, 0, 0, 42, 0}, true},
		{"animation", []byte{'A', 'N', 'I', 'M', 0, 0, 0, 0}, false},
		{"animation-word-in-payload", []byte{'J', 'U', 'N', 'K', 4, 0, 0, 0, 'A', 'N', 'I', 'M'}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bytes := append(append([]byte{}, raw...), tc.tail...)
			binary.LittleEndian.PutUint32(bytes[4:8], uint32(len(bytes)-8))
			_, err := imageBytes(p, bytes, "image/webp")
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	for _, flags := range []byte{0, 2, 0x20} {
		extended := append([]byte{}, raw[:12]...)
		extended = append(extended, []byte{'V', 'P', '8', 'X', 10, 0, 0, 0, flags, 0, 0, 0, 0, 0, 0, 0, 0, 0}...)
		extended = append(extended, raw[12:]...)
		binary.LittleEndian.PutUint32(extended[4:8], uint32(len(extended)-8))
		_, err := imageBytes(p, extended, "image/webp")
		if flags == 0 {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}
