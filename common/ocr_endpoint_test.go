package common

import (
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestOCREndpointInference(t *testing.T) {
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypePaddleOCR}, GetEndpointTypesByChannelType(constant.ChannelTypePaddleOCR, "PaddleOCR-VL-1.6"))
	info, ok := GetDefaultEndpointInfo(constant.EndpointTypePaddleOCR)
	assert.True(t, ok)
	assert.Equal(t, "/api/v2/ocr/jobs", info.Path)
}
