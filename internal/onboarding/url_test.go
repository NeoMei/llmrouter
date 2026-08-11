package onboarding

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNormalizeServerURL(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"https://stage.example/v1/", "https://stage.example", true},
		{"http://127.0.0.1:13100", "http://127.0.0.1:13100", true},
		{"http://stage.example", "", false},
		{"https://user:pass@stage.example", "", false},
		{"https://stage.example?token=secret", "", false},
		{"https://stage.example/path", "", false},
		{"ftp://stage.example", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			u, err := NormalizeServerURL(tc.in)
			if tc.ok {
				require.NoError(t, err)
				require.Equal(t, tc.want, u.String())
			} else {
				require.Error(t, err)
			}
		})
	}
}
