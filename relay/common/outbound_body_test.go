package common

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOutboundJSONBodyReturnsIndependentGetBody(t *testing.T) {
	payload := []byte(`{"model":"gpt-test","input":"hello"}`)
	body, size, getBody, closer, err := NewOutboundJSONBody(payload)
	require.NoError(t, err)
	defer closer.Close()
	assert.Equal(t, int64(len(payload)), size)

	primary, err := io.ReadAll(body)
	require.NoError(t, err)
	a, err := getBody()
	require.NoError(t, err)
	defer a.Close()
	b, err := getBody()
	require.NoError(t, err)
	defer b.Close()
	gotA, err := io.ReadAll(a)
	require.NoError(t, err)
	gotB, err := io.ReadAll(b)
	require.NoError(t, err)
	assert.Equal(t, payload, primary)
	assert.Equal(t, payload, gotA)
	assert.Equal(t, payload, gotB)
}
