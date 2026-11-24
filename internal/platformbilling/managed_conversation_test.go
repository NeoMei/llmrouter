package platformbilling

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagedConversationOperationFromWireContent(t *testing.T) {
	for _, tc := range []struct {
		name, wire, body, want string
	}{
		{"plain and opaque tool arguments", "chat.completions.create", `{"messages":[{"content":"inspect image_url","tool_calls":[{"function":{"arguments":{"type":"image_url"}}}]}],"metadata":{"type":"image_url"}}`, "chat"},
		{"chat image", "chat.completions.create", `{"Messages":[{"Content":[{"type":"image_url","image_url":{"url":"https://fixture.test/image"}}]}]}`, "vision"},
		{"Claude tool result image", "claude.messages.create", `{"messages":[{"content":[{"type":"tool_result","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}]}`, "vision"},
		{"Gemini nested function result image", "gemini.generate", `{"contents":[{"parts":[{"functionResponse":{"parts":[{"inline_data":{"mime_type":"image/png","data":"AA=="}}]}}]}]}`, "vision"},
		{"Gemini audio is not an image", "gemini.generate", `{"contents":[{"parts":[{"inlineData":{"mimeType":"audio/wav","data":"AA=="}}]}]}`, "chat"},
		{"Responses image", "responses.create", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://fixture.test/image"}]}]}`, "vision"},
		{"compact tool output image", "responses.compact", `{"input":[{"type":"function_call_output","output":[{"type":"input_image","image_url":"https://fixture.test/image"}]}]}`, "vision"},
		{"Responses text", "responses.create", `{"input":"inspect input_image"}`, "chat"},
		{"unrelated wire", "images.generations.create", `{"prompt":"image"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := managedConversationOperation(tc.wire, []byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
