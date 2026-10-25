package dto

import "encoding/json"

// Native and canonical creation share exactly one input contract. File bytes
// use multipart; JSON supports fileUrl only. No client routing fields exist.
type OCRCreateRequest struct {
	Model           string          `json:"model"`
	FileURL         string          `json:"fileUrl,omitempty"`
	OptionalPayload json.RawMessage `json:"optionalPayload,omitempty"`
}
type OCRJobResponse struct {
	ID             string     `json:"id"`
	Object         string     `json:"object"`
	Model          string     `json:"model"`
	State          string     `json:"state"`
	Asynchronous   bool       `json:"asynchronous"`
	TotalPages     int        `json:"total_pages"`
	ExtractedPages int        `json:"extracted_pages"`
	ResultURL      string     `json:"result_url,omitempty"`
	Result         *OCRResult `json:"result,omitempty"`
}
type OCRResult struct {
	Pages []OCRPage `json:"pages"`
	// Raw native JSONL records retain tables/formulas and extensions losslessly.
	Native []json.RawMessage `json:"native"`
}
type OCRPage struct {
	Width    *float64         `json:"width,omitempty"`
	Height   *float64         `json:"height,omitempty"`
	Index    *int             `json:"index,omitempty"`
	Markdown *OCRMarkdown     `json:"markdown,omitempty"`
	Layout   []OCRLayoutBlock `json:"layout,omitempty"`
	Raw      json.RawMessage  `json:"raw"`
}
type OCRMarkdown struct {
	Text   string            `json:"text"`
	Images map[string]string `json:"images,omitempty"`
}
type OCRLayoutBlock struct {
	Label   string          `json:"label"`
	Content string          `json:"content"`
	BBox    []float64       `json:"bbox,omitempty"`
	Raw     json.RawMessage `json:"raw"`
}
