package doubao

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSeedanceUsageRates(t *testing.T) {
	for _, tc := range []struct {
		model, resolution string
		inputVideo        bool
		rate              float64
	}{
		{"doubao-seedance-2-5-260628", "720p", true, 42.0 / 70.0},
		{"doubao-seedance-2-5-260628", "1080p", false, 77.0 / 70.0},
		{"doubao-seedance-2-5-260628", "4K", true, 19.0 / 70.0},
		{"doubao-seedance-2-5-flash-261115", "480p", true, 28.0 / 46.0},
		{"doubao-seedance-2-0-mini-260615", "720p", true, 14.0 / 23.0},
		{"doubao-seedance-2-0-260128", "1080p", true, 31.0 / 46.0},
	} {
		t.Run(tc.model+"/"+tc.resolution, func(t *testing.T) {
			rate, ok := GetVideoInputRatio(tc.model, tc.resolution, tc.inputVideo)
			assert.True(t, ok)
			assert.InDelta(t, tc.rate, rate, 1e-10)
		})
	}
}
