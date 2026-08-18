package oairesponses

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesRequestToGeminiNormalizesToolIDsAndPreservesBigInt(t *testing.T) {
	got, err := OpenAIResponsesRequestToGeminiChat(nil, responsesRequestWithToolHistory("bad/id", `{"n":184467440737095516160}`), nil)
	require.NoError(t, err)
	call := got.Contents[0].Parts[0].FunctionCall
	result := got.Contents[1].Parts[0].FunctionResponse
	require.NotNil(t, call)
	require.NotNil(t, result)
	assert.NotEqual(t, "bad/id", string(call.ID))
	assert.Equal(t, call.ID, result.ID)
	raw, err := common.Marshal(call.Arguments)
	require.NoError(t, err)
	assert.Equal(t, `{"n":184467440737095516160}`, string(raw))
}

func TestOpenAIResponsesRequestToGeminiRejectsMalformedArguments(t *testing.T) {
	for _, args := range []string{`{bad-json`, `[]`, `{"x":1,"x":2}`} {
		t.Run(args, func(t *testing.T) {
			_, err := OpenAIResponsesRequestToGeminiChat(nil, responsesRequestWithToolHistory("call_1", args), nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "arguments")
		})
	}
}

func TestOpenAIResponsesRequestToGeminiRejectsUnknownToolResultID(t *testing.T) {
	req := responsesRequestWithToolHistory("call_1", `{}`)
	req.Input = []byte(`[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"unknown","output":"ok"}]`)
	_, err := OpenAIResponsesRequestToGeminiChat(nil, req, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool result id")
}

var _ = dto.GeminiChatRequest{}
