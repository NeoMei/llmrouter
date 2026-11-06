package router

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestManagedNativeResourcesRequireOwnedReferences(t *testing.T) {
	for _, body := range []string{
		`{"input":[{"type":"input_file","file_id":"file_private"}]}`,
		`{"input":[{"type":"item_reference","id":"item_private"}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_file","file_id":"file_private"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file_private"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file_private"}}]}]}`,
		`{"contents":[{"parts":[{"fileData":{"fileUri":"files/private"}}]}]}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_private"]}]}`,
		`{"tools":[{"type":"code_interpreter","container":"cntr_private"}]}`,
		`{"tools":[{"type":"code_interpreter","container":{"type":"auto","file_ids":["file_private"]}}]}`,
		`{"tools":[{"retrieval":{"vertexRagStore":{"ragResources":[{"ragCorpus":"private"}]}}}]}`,
		`{"background":true}`,
		`{"prompt":{"id":"pmpt_private","version":"1"},"input":"hello"}`,
	} {
		require.Error(t, validateManagedNativeResources([]byte(body), "responses.create"), body)
	}
}

func TestManagedNativeResourcesKeepInlineAndUserToolPayloads(t *testing.T) {
	for _, body := range []string{
		`{"input":"describe file_id and container_id"}`,
		`{"input":[{"role":"user","content":[{"type":"input_file","file_data":"data:application/pdf;base64,dGVzdA=="}]}]}`,
		`{"input":[{"type":"function_call","arguments":"{\"file_id\":\"user-data\"}"},{"type":"function_call_output","output":"file_id"}]}`,
		`{"messages":[{"role":"user","content":[{"type":"tool_use","input":{"file_id":"user-data"}},{"type":"image","source":{"type":"base64","data":"dGVzdA=="}}]}]}`,
		`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"dGVzdA=="}}]}]}`,
		`{"tools":[{"type":"function","parameters":{"properties":{"file_id":{"type":"string"}}}}]}`,
		`{"tools":[{"type":"code_interpreter","container":"auto"}]}`,
		`{"background":false}`,
	} {
		require.NoError(t, validateManagedNativeResources([]byte(body), "responses.create"), body)
	}
}
