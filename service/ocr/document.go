package ocr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/QuantumNous/new-api/model"
	base "github.com/QuantumNous/new-api/service"
	"io"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/dto"
)

type Document struct {
	ID    string         `json:"id"`
	Pages []DocumentPage `json:"pages"`
}
type DocumentPage struct {
	Index  *int            `json:"index,omitempty"`
	Width  *float64        `json:"width,omitempty"`
	Height *float64        `json:"height,omitempty"`
	Text   string          `json:"text"`
	Layout []DocumentBlock `json:"layout"`
}
type DocumentBlock struct {
	Label   string    `json:"label"`
	Content string    `json:"content"`
	BBox    []float64 `json:"bbox,omitempty"`
}

var markdownImage = regexp.MustCompile(`!\[[^\]]*\]\([^\r\n]*\)|!\[[^\]]*\]\[[^\]]*\]`)
var markdownLink = regexp.MustCompile(`\[([^\]]*)\]\([^\r\n)]*\)`)
var htmlImage = regexp.MustCompile(`(?is)<[^>]*>`)
var referenceLink = regexp.MustCompile(`(?m)^\s*\[[^\]]+\]:[^\r\n]*`)
var resourceURL = regexp.MustCompile(`(?i)(?:https?://|//|data:|file:)[^\s<>"\x27]*`)

// ProjectDocument retains recognized text and layout, without provider payloads
// or capabilities that could fetch a private media resource.
func ProjectDocument(id string, source *dto.OCRResult) Document {
	out := Document{ID: id, Pages: make([]DocumentPage, 0, len(source.Pages))}
	for _, page := range source.Pages {
		resources := []string{}
		if page.Markdown != nil {
			for _, value := range page.Markdown.Images {
				if value != "" {
					resources = append(resources, value)
				}
			}
		}
		clean := func(value string) string {
			value = markdownImage.ReplaceAllString(value, "")
			value = markdownLink.ReplaceAllString(value, "$1")
			value = htmlImage.ReplaceAllString(value, "")
			value = referenceLink.ReplaceAllString(value, "")
			value = resourceURL.ReplaceAllString(value, "")
			for _, resource := range resources {
				value = strings.ReplaceAll(value, resource, "")
			}
			return value
		}
		next := DocumentPage{Index: page.Index, Width: page.Width, Height: page.Height, Layout: []DocumentBlock{}}
		if page.Markdown != nil {
			next.Text = clean(page.Markdown.Text)
		}
		for _, block := range page.Layout {
			next.Layout = append(next.Layout, DocumentBlock{Label: clean(block.Label), Content: clean(block.Content), BBox: block.BBox})
		}
		out.Pages = append(out.Pages, next)
	}
	return out
}

// ReadDocument is a text-only read after the HTTP adapter verifies the current
// durable API key. It issues no bearer resource grants or supplier requests.
func (s *Service) ReadDocument(ctx context.Context, id, callID string, userID, tokenID int) (Document, error) {
	invalid := Document{}
	snapshot, err := s.billing.Inspect(ctx, id)
	if err != nil {
		return invalid, err
	}
	j := snapshot.Job
	if j.UserID != userID || j.TokenID != tokenID || j.Funding != "platform" || j.PlatformCallID != callID || j.ProviderState != "done" || j.ResultResourceID == "" || base.PlatformTaskSettlementCheck == nil || base.PlatformTaskSettlementCheck(ctx, callID) != nil {
		return invalid, model.ErrOCRConflict
	}
	resource, err := s.billing.OCRResource(ctx, id, j.ResultResourceID)
	if err != nil {
		return invalid, err
	}
	if resource.Kind != "jsonl" || resource.Revoked || resource.ExpiresAt <= s.now().Unix() || resource.Size < 1 || resource.Size > MaxResultBytes {
		return invalid, model.ErrOCRGrant
	}
	body, stored, err := s.objects.Open(ctx, resource.ObjectKey)
	if err != nil {
		return invalid, err
	}
	defer body.Close()
	if stored.Size != resource.Size {
		return invalid, errors.New("OCR stored resource size mismatch")
	}
	data, err := io.ReadAll(io.LimitReader(body, MaxResultBytes+1))
	if err != nil || int64(len(data)) != resource.Size {
		return invalid, errors.New("invalid stored OCR result")
	}
	checksum := sha256.Sum256(data)
	if hex.EncodeToString(checksum[:]) != resource.Checksum {
		return invalid, errors.New("OCR stored resource checksum mismatch")
	}
	pages, err := parseStoredPages(data)
	if err != nil {
		return invalid, err
	}
	if len(pages.Pages) == 0 || len(pages.Pages) > 1000 {
		return invalid, errors.New("invalid OCR document pages")
	}
	document := ProjectDocument(callID, pages)
	current, err := s.billing.OCRResource(ctx, id, resource.ID)
	if err != nil || current != resource || current.Revoked || current.ExpiresAt <= s.now().Unix() {
		return invalid, model.ErrOCRGrant
	}
	return document, nil
}
