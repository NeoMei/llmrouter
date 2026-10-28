package dto

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestKnowledgeEmbeddingFourShapes(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		count int
	}{
		{`"hello"`, 1}, {`["hello","world"]`, 2}, {`[0,2147483647]`, 1}, {`[[0],[1,2]]`, 2},
		{`[]`, 0}, {`[[]]`, 0}, {`[1.5]`, 0}, {`[-1]`, 0}, {`[2147483648]`, 0}, {`[1e100]`, 0}, {`["hello",1]`, 0}, {`[[1],"x"]`, 0}, {`[null]`, 0},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var input any
			require.NoError(t, common.UnmarshalUnique([]byte(tc.raw), &input))
			n, err := (&EmbeddingRequest{Input: input}).InputCount()
			if tc.count == 0 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.count, n)
			}
		})
	}
	for _, input := range []any{[]int{2147483648}, [][]int{{2147483648}}, []any{json.Number("-1")}} {
		_, err := (&EmbeddingRequest{Input: input}).InputCount()
		require.Error(t, err)
	}
}
