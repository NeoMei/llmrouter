package helper

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/relaymedia"
)

const MultimodalPreparedKey = "internal.multimodal_prepared"
const MultimodalTextReserve = 64_000

// PreparedMultimodal stays request-private, containing only frozen supplier
// inputs and facts; source URLs never appear in response or logging metadata.
type PreparedMultimodal struct {
	Contents      []dto.MultimodalEmbeddingContent
	Images        []MediaFact
	Video         *relaymedia.Prepared
	VisualReserve int
}

func MultimodalVisualReserve(r *dto.MultimodalEmbeddingRequest) (int, error) {
	visual := false
	for _, v := range r.Input {
		visual = visual || v.Image != "" || v.Video != ""
	}
	if !visual {
		return 0, nil
	}
	n, err := strconv.Atoi(os.Getenv("QWEN_MM_VISUAL_RESERVE_TOKENS"))
	if err != nil || n < 1 || n > 10_000_000 {
		return 0, fmt.Errorf("visual embeddings require QWEN_MM_VISUAL_RESERVE_TOKENS between 1 and 10000000")
	}
	return n, nil
}
func PrepareMultimodalImages(ctx context.Context, r *dto.MultimodalEmbeddingRequest, reserve int) (*PreparedMultimodal, error) {
	p := &PreparedMultimodal{Contents: append([]dto.MultimodalEmbeddingContent(nil), r.Input...), VisualReserve: reserve}
	total := 0
	for i, item := range p.Contents {
		if item.Image == "" {
			continue
		}
		carrier := item.Image
		var data []byte
		if strings.HasPrefix(carrier, "https://") || strings.HasPrefix(carrier, "http://") {
			u, err := url.Parse(carrier)
			if err != nil || u.User != nil || u.Fragment != "" || u.Host == "" {
				return nil, fmt.Errorf("invalid image URL")
			}
			if service.ValidateSSRFProtectedFetchURL(carrier) != nil {
				return nil, fmt.Errorf("image URL is not permitted")
			}
			base := service.GetSSRFProtectedHTTPClient()
			if base == nil {
				return nil, fmt.Errorf("protected image fetch unavailable")
			}
			client := *base
			client.Jar = nil
			client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				if req.URL.User != nil || req.URL.Fragment != "" || len(via) >= 10 {
					return fmt.Errorf("invalid image redirect")
				}
				if err := service.ValidateSSRFProtectedFetchURL(req.URL.String()); err != nil {
					return err
				}
				if base.CheckRedirect != nil {
					return base.CheckRedirect(req, via)
				}
				return nil
			}
			fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			req, _ := http.NewRequestWithContext(fetchCtx, http.MethodGet, carrier, nil)
			resp, err := client.Do(req)
			if err != nil {
				cancel()
				return nil, fmt.Errorf("protected image fetch failed")
			}
			if resp.StatusCode != 200 || resp.ContentLength > 10_000_000 {
				resp.Body.Close()
				cancel()
				return nil, fmt.Errorf("image fetch exceeds limit or failed")
			}
			data, err = io.ReadAll(io.LimitReader(resp.Body, 10_000_001))
			resp.Body.Close()
			cancel()
			if err != nil || len(data) > 10_000_000 {
				return nil, fmt.Errorf("image fetch exceeds limit or failed")
			}
		} else {
			head, encoded, ok := strings.Cut(carrier, ",")
			if !ok || !strings.HasPrefix(head, "data:") || !strings.HasSuffix(head, ";base64") || len(encoded) > base64.StdEncoding.EncodedLen(10_000_000) {
				return nil, fmt.Errorf("image requires bounded PNG/JPEG data URI or protected URL")
			}
			var err error
			data, err = base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(data) > 10_000_000 {
				return nil, fmt.Errorf("invalid or oversized image bytes")
			}
		}
		total += len(data)
		if total > 100_000_000 {
			return nil, fmt.Errorf("aggregate media exceeds limit")
		}
		carrier = "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		fact, err := DecodeMediaFact("image", carrier)
		if err != nil || (fact.Format != "png" && fact.Format != "jpeg") || fact.Pixels > 16_000_000 {
			return nil, fmt.Errorf("image requires decoded PNG/JPEG within 16 million pixels")
		}
		if fact.Format == "jpeg" {
			carrier = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
		}
		p.Images = append(p.Images, fact)
		p.Contents[i].Image = carrier
	}
	return p, nil
}
func ValidatePreparedMultimodal(p *PreparedMultimodal, m dto.ModelCapabilities) error {
	l := m.MediaLimits
	if l == nil {
		return nil
	}
	if l.MaxImages != nil && len(p.Images) > *l.MaxImages {
		return fmt.Errorf("image count exceeds declared max_images")
	}
	for _, f := range p.Images {
		if len(l.Formats) > 0 && !containsCapability(l.Formats, f.Format) {
			return fmt.Errorf("image format not declared")
		}
		if l.MaxPixels != nil && f.Pixels > *l.MaxPixels {
			return fmt.Errorf("image exceeds declared max_pixels")
		}
	}
	if p.Video != nil {
		f := p.Video.Facts
		if f.Format == "" || f.Width <= 0 || f.Height <= 0 || f.Seconds <= 0 || !f.AudioKnown {
			return fmt.Errorf("video facts unavailable")
		}
		if len(l.Formats) > 0 && !containsCapability(l.Formats, f.Format) {
			return fmt.Errorf("video format not declared")
		}
		if l.MaxPixels != nil && int64(f.Width)*int64(f.Height) > *l.MaxPixels {
			return fmt.Errorf("video exceeds declared max_pixels")
		}
		if l.MaxVideoSeconds != nil && f.Seconds > *l.MaxVideoSeconds {
			return fmt.Errorf("video exceeds declared max_video_seconds")
		}
		if l.VideoAudio != nil && !*l.VideoAudio && f.HasAudio {
			return fmt.Errorf("video audio forbidden")
		}
	}
	return nil
}
