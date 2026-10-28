package platformbilling

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOCRConflictingOutputCannotSettle(t *testing.T) {
	a, db, settles := meteredFixture(t, "speech")
	ctx := context.Background()
	s, e := a.AdmitOperation(ctx, "uh_model_token", "task", "call", "speech", "application/json", []byte(`{"model":"test","input":"hello","voice":"alloy"}`))
	require.NoError(t, e)
	require.NoError(t, s.Dispatch(ctx, 1))
	first := []byte(`{"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3},"text":"A"}`)
	require.NoError(t, s.RecordOCRObservation(ctx, first, true))
	require.NoError(t, s.RecordOCRObservation(ctx, first, true))
	require.ErrorIs(t, s.RecordOCRObservation(ctx, []byte(`{"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3},"text":"B"}`), true), ErrConflict)
	require.ErrorIs(t, s.ObserveMetering(ctx, speechUsage("2")), ErrConflict)
	require.ErrorIs(t, s.Finish(ctx), ErrConflict)
	assert.Zero(t, settles.Load())
	var call Call
	require.NoError(t, db.First(&call).Error)
	assert.True(t, call.EvidenceConflict)
	assert.Equal(t, "UNKNOWN", call.Status)
}

func TestQwenOCRProviderTokenPolicyUsesExactNativeModelAndUsage(t *testing.T) {
	var imageBytes bytes.Buffer
	require.NoError(t, png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	keyHash := sha256.Sum256([]byte("synthetic-paddle-key"))
	policy := OCRPolicy{
		Version: "ocr_v1", PublicModel: "qwen3.5-ocr", WireOperation: "dashscope.ocr.recognitions.create",
		ChannelID: 1, ChannelType: constant.ChannelTypeAli, ChannelKeySHA256: hex.EncodeToString(keyHash[:]),
		UpstreamModel: "qwen3.5-ocr", Origin: "https://dashscope.aliyuncs.com", APIversion: "dashscope-advanced-recognition-v1",
		UsagePolicy: "provider_tokens_v1", MaxInputTokens: 1000, MaxOutputTokens: 100,
		MaxImageBytes: 10 << 20, MaxImagePixels: 16 * 1024 * 1024, BlankPolicy: "reject_empty_v1",
		RateIDs: map[string]string{"CNY/6": "ocr-token-rate"},
	}
	config := Config{MaxBodyBytes: 1 << 20, OCRPolicies: map[string]map[string]OCRPolicy{"dashscope.ocr.recognitions.create": {"qwen3.5-ocr": policy}}}
	body := []byte(fmt.Sprintf(`{"model":"qwen3.5-ocr","input":{"messages":[{"role":"user","content":[{"image":"data:image/png;base64,%s","enable_rotate":false}]}]},"parameters":{"ocr_options":{"task":"advanced_recognition"}}}`, base64.StdEncoding.EncodeToString(imageBytes.Bytes())))
	request, err := parseQwenOCR(config, "application/json", body)
	require.NoError(t, err)
	assert.Equal(t, 1000, request.Quantity)
	assert.Equal(t, 100, request.Output)

	rate := Rate{ID: "ocr-token-rate", Model: "qwen3.5-ocr", Asset: "CNY/6", Status: "published", Semantics: "metered_v2", Metered: &metering.Price{
		Version: "metered_v2", Operation: "ocr", Policy: "provider_tokens_v1", Terms: []metering.Term{
			{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "2"}},
			{Key: "output", Unit: "token", Price: metering.Rational{Numerator: "2", Denominator: "1"}},
		},
	}}
	rateSnapshot, err := common.Marshal(rate)
	require.NoError(t, err)
	session := &Session{adapter: &Adapter{config: config}, canonical: request.Canonical, call: Call{
		Model: "qwen3.5-ocr", WireOperation: policy.WireOperation, MediaSnapshot: request.MediaSnapshot,
		RateSnapshot: string(rateSnapshot), ReservationQuantity: int64(request.Quantity), ReservationOutput: int64(request.Output),
	}}
	effective, err := session.QwenOCRBody()
	require.NoError(t, err)
	assert.Contains(t, string(effective), `"model":"qwen3.5-ocr"`)

	valid := []byte(`{"request_id":"provider-request","output":{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":[{"ocr_result":{"words_info":[{"text":"OK","rotate_rect":[1,1,2,1,0]}]}}]}}]},"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
	usage, err := session.QwenOCRUsage(valid)
	require.NoError(t, err)
	assert.Equal(t, "provider_tokens_v1", usage.Source)
	require.Len(t, usage.Measurements, 2)
	assert.Equal(t, metering.Measurement{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: "3"}}, usage.Measurements[0])
	assert.Equal(t, metering.Measurement{Key: "output", Unit: "token", Quantity: metering.Quantity{Value: "2"}}, usage.Measurements[1])

	over := []byte(`{"request_id":"provider-request","output":{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":[{"ocr_result":{"words_info":[{"text":"OK","rotate_rect":[1,1,2,1,0]}]}}]}}]},"usage":{"input_tokens":1001,"output_tokens":2,"total_tokens":1003}}`)
	_, err = session.QwenOCRUsage(over)
	require.ErrorIs(t, err, ErrInvalid)
}
