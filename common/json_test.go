package common

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJsonRawMessageToString(t *testing.T) {
	tests := []struct {
		name string
		data json.RawMessage
		want string
	}{
		{
			name: "object",
			data: json.RawMessage(`{"city":"Paris","days":0,"strict":false}`),
			want: `{"city":"Paris","days":0,"strict":false}`,
		},
		{
			name: "string",
			data: json.RawMessage(`"{\"city\":\"Paris\",\"days\":0,\"strict\":false}"`),
			want: `{"city":"Paris","days":0,"strict":false}`,
		},
		{
			name: "null",
			data: json.RawMessage(`null`),
			want: "",
		},
		{
			name: "empty",
			data: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, JsonRawMessageToString(tt.data))
		})
	}
}

func TestUnmarshalUseNumberRejectsTrailingJSON(t *testing.T) {
	var value map[string]any
	err := UnmarshalUseNumber([]byte(`{"count":9007199254740993} trailing`), &value)
	require.Error(t, err)
}

func TestNewJsonDecoderPreservesBufferedSequentialValues(t *testing.T) {
	decoder := NewJsonDecoder(strings.NewReader(`{"first":1}{"second":2}`))
	var first, second json.RawMessage
	require.NoError(t, decoder.Decode(&first))
	require.NoError(t, decoder.Decode(&second))
	require.JSONEq(t, `{"first":1}`, string(first))
	require.JSONEq(t, `{"second":2}`, string(second))
	require.ErrorIs(t, decoder.Decode(&json.RawMessage{}), io.EOF)
}
