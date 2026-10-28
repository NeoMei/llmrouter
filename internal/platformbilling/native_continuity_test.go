package platformbilling

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNativeContinuityInputUnion(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","input":"next","previous_response_id":null}`,
		`{"model":"m","input":[{"type":"compaction","id":null,"encrypted_content":"opaque\\nbytes"}]}`,
		`{"model":"m","input":[{"id":"item-owned"}]}`,
	} {
		_, err := strictResponsesRequest([]byte(body))
		require.NoError(t, err, body)
	}
}

func TestNativeSelectorsAreSchemaScoped(t *testing.T) {
	selectors, err := nativeSelectors([]byte(`{"previous_response_id":null,"input":[{"role":"user","content":"previous_response_id: foreign"},{"type":"function_call_output","call_id":"f","output":"{\"id\":\"other\"}"},{"type":"compaction","id":"inline-only","encrypted_content":"opaque"},{"id":"actual-item"}]}`))
	require.NoError(t, err)
	require.Len(t, selectors, 1)
	require.Equal(t, "actual-item", selectors[0].ID)
	for _, body := range []string{`{"previous_response_id":"bad\nID"}`, `{"input":[{"type":"item_reference","id":""}]}`} {
		_, err := nativeSelectors([]byte(body))
		require.ErrorIs(t, err, ErrNativeHistory)
	}
}
