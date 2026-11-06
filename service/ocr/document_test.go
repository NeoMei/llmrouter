package ocr

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDocumentProjectionExcludesResourcesAndRawProviderFields(t *testing.T) {
	text := "A table | x | y |\nEquation $x^2$ [caption](/api/resources/secret?grant=private)\n![scan](https://supplier.invalid/object?grant=secret)\n<img src=\"https://supplier.invalid/object?grant=secret\">\n![scan][scan]\n[scan]: https://original.invalid/secret?credential=private\nRead https://docs.example/reference"
	source := &dto.OCRResult{Pages: []dto.OCRPage{{Markdown: &dto.OCRMarkdown{Text: text, Images: map[string]string{"scan.png": "https://supplier.invalid/object?grant=secret"}}, Layout: []dto.OCRLayoutBlock{{Label: "text https://label.invalid/signed?credential=private", Content: text, BBox: []float64{1, 2, 3, 4}, Raw: []byte(`{"secret":"private"}`)}}, Raw: []byte(`{"secret":"private"}`)}}}
	out := ProjectDocument("call-1", source)
	require.Len(t, out.Pages, 1)
	raw, err := common.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "supplier.invalid")
	assert.NotContains(t, string(raw), "grant=secret")
	assert.NotContains(t, string(raw), "private")
	assert.NotContains(t, string(raw), "images")
	assert.Contains(t, out.Pages[0].Text, "A table | x | y |")
	assert.Contains(t, out.Pages[0].Text, "$x^2$ caption")
	assert.NotContains(t, string(raw), "https://")
	assert.NotContains(t, string(raw), "original.invalid")
	assert.Equal(t, []float64{1, 2, 3, 4}, out.Pages[0].Layout[0].BBox)
}
