package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
)

// CanonicalResult projects actual stored native pages, without supplier calls or
// synthetic scores/coordinates. Unknown native structures remain raw JSON.
func (s *Service) CanonicalResult(ctx context.Context, id string, userID, tokenID int) (*dto.OCRResult, error) {
	snapshot, err := s.billing.Inspect(ctx, id)
	if err != nil {
		return nil, err
	}
	j := snapshot.Job
	if j.UserID != userID || j.TokenID != tokenID {
		return nil, model.ErrOCRConflict
	}
	if j.ResultResourceID == "" {
		return nil, errors.New("OCR result pending")
	}
	resource, err := s.billing.OCRResource(ctx, id, j.ResultResourceID)
	if err != nil {
		return nil, err
	}
	rawURL, err := s.resourceURL(ctx, resource)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	body, _, err := s.OpenGrantedResource(ctx, id, resource.ID, u.Query().Get("token"))
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, MaxResultBytes+1))
	if err != nil || len(data) > MaxResultBytes {
		return nil, errors.New("invalid stored OCR result")
	}
	result := &dto.OCRResult{Pages: []dto.OCRPage{}, Native: []json.RawMessage{}}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var native struct {
			Result struct {
				Pages []json.RawMessage `json:"layoutParsingResults"`
			} `json:"result"`
		}
		if err := common.Unmarshal(line, &native); err != nil {
			return nil, err
		}
		result.Native = append(result.Native, append(json.RawMessage(nil), line...))
		for _, raw := range native.Result.Pages {
			page := dto.OCRPage{Raw: raw}
			var fields map[string]json.RawMessage
			if err := common.Unmarshal(raw, &fields); err != nil {
				return nil, err
			}
			// Optional projections never reject a future provider extension: exact raw
			// remains available even when a known field changes shape.
			if b := fields["markdown"]; len(b) > 0 {
				var markdown dto.OCRMarkdown
				if common.Unmarshal(b, &markdown) == nil {
					page.Markdown = &markdown
				}
			}
			var pruned struct {
				Width  *float64          `json:"width"`
				Height *float64          `json:"height"`
				Index  *int              `json:"page_index"`
				Blocks []json.RawMessage `json:"parsing_res_list"`
			}
			if common.Unmarshal(fields["prunedResult"], &pruned) == nil {
				page.Index = pruned.Index
				page.Width, page.Height = pruned.Width, pruned.Height
				for _, block := range pruned.Blocks {
					var b struct {
						Label   string    `json:"block_label"`
						Content string    `json:"block_content"`
						BBox    []float64 `json:"block_bbox"`
					}
					if common.Unmarshal(block, &b) == nil {
						page.Layout = append(page.Layout, dto.OCRLayoutBlock{Label: b.Label, Content: b.Content, BBox: b.BBox, Raw: block})
					}
				}
			}
			result.Pages = append(result.Pages, page)
		}
	}
	return result, nil
}
