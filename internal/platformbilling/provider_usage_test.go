package platformbilling

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestProviderUsageAliasesRequireAgreement(t *testing.T) {
	for _, tc := range []struct {
		usage string
		valid bool
	}{
		{`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"total_characters":0,"prompt_tokens_details":{"cached_tokens":2}}`, true},
		{`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":8}`, true},
		{`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cached_tokens":2,"prompt_tokens_details":{"cached_tokens":2}}`, true},
		{`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cached_tokens":3,"prompt_tokens_details":{"cached_tokens":2}}`, false},
		{`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"total_characters":1}`, false},
		{`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":9}`, false},
	} {
		s := &Session{}
		s.ObserveRawEnvelope([]byte(`{"usage":`+tc.usage+`}`), false)
		if tc.valid {
			require.NotNil(t, s.confirmed, tc.usage)
			require.Equal(t, "2", s.confirmed.CacheRead)
		} else {
			require.Nil(t, s.confirmed, tc.usage)
		}
	}
}
