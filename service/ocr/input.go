package ocr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const (
	MaxInputBytes       = 20 << 20
	MaxResultBytes      = 32 << 20
	MaxImageBytes       = 10 << 20
	MaxTotalResultBytes = 64 << 20
	MaxPages            = 200
	MaxImages           = 256
	MaxImagePixels      = 25_000_000
)

// No pageRanges, filesystem paths, arbitrary options or client page counts.
type Input struct {
	File              []byte
	Filename, FileURL string
	OptionalPayload   json.RawMessage
}
type validatedInput struct {
	data                          []byte
	filename, contentType, digest string
	pages                         int
	optional                      json.RawMessage
	urlMode                       bool
}

func validateImage(data []byte) (string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width) > MaxImagePixels/int64(cfg.Height) {
		return "", errors.New("OCR image type or pixel bound invalid")
	}
	// Decode after dimension admission; reject truncated/corrupt payloads too.
	img, decoded, err := image.Decode(bytes.NewReader(data))
	if err != nil || decoded != format || img.Bounds().Dx() != cfg.Width || img.Bounds().Dy() != cfg.Height {
		return "", errors.New("OCR image decode failed")
	}
	if format == "jpeg" {
		return "image/jpeg", nil
	}
	return "image/png", nil
}
func (s *Service) validateInput(ctx context.Context, in Input) (validatedInput, error) {
	var out validatedInput
	if (len(in.File) == 0) == (in.FileURL == "") {
		return out, errors.New("exactly one OCR file or fileUrl required")
	}
	if len(in.OptionalPayload) > 8192 {
		return out, errors.New("OCR optionalPayload too large")
	}
	opts := map[string]json.RawMessage{}
	if len(in.OptionalPayload) > 0 {
		if err := common.Unmarshal(in.OptionalPayload, &opts); err != nil || opts == nil {
			return out, errors.New("invalid OCR optionalPayload")
		}
	}
	for k, v := range opts {
		switch k {
		case "useOcrForImageBlock", "relevelTitles", "layoutNms", "useLayoutDetection", "visualize", "prettifyMarkdown", "mergeTables", "restructurePages", "useDocOrientationClassify", "useDocUnwarping", "useTextlineOrientation", "useChartRecognition", "useSealRecognition", "useTableRecognition", "useFormulaRecognition":
			var b bool
			if string(v) != "true" && string(v) != "false" {
				return out, errors.New("OCR option requires boolean")
			}
			if err := common.Unmarshal(v, &b); err != nil {
				return out, err
			}
		case "markdownIgnoreLabels":
			var labels []string
			if err := common.Unmarshal(v, &labels); err != nil || labels == nil || len(labels) > 32 {
				return out, errors.New("invalid OCR markdown labels")
			}
			for _, label := range labels {
				if len(label) == 0 || len(label) > 64 || strings.ContainsAny(label, "\r\n\x00") {
					return out, errors.New("invalid OCR markdown label")
				}
			}
		case "layoutShapeMode", "promptLabel":
			var text string
			if err := common.Unmarshal(v, &text); err != nil || (k == "layoutShapeMode" && text != "auto") || (k == "promptLabel" && text != "ocr") {
				return out, errors.New("unsupported OCR option value")
			}
		case "repetitionPenalty", "temperature", "topP":
			var n float64
			if err := common.Unmarshal(v, &n); err != nil || string(v) == "null" || math.IsNaN(n) || math.IsInf(n, 0) {
				return out, errors.New("invalid OCR sampling option")
			}
			if (k == "repetitionPenalty" && (n <= 0 || n > 2)) || (k == "temperature" && (n < 0 || n > 2)) || (k == "topP" && (n <= 0 || n > 1)) {
				return out, errors.New("OCR sampling option out of bounds")
			}
		case "minPixels", "maxPixels":
			var n int
			if err := common.Unmarshal(v, &n); err != nil || n < 1 || n > MaxImagePixels {
				return out, errors.New("OCR pixel option out of bounds")
			}
		default:
			return out, errors.New("unsupported OCR option")
		}
	}
	if lo, ok := opts["minPixels"]; ok {
		if hi, ok := opts["maxPixels"]; ok {
			var minPixels, maxPixels int
			_ = common.Unmarshal(lo, &minPixels)
			_ = common.Unmarshal(hi, &maxPixels)
			if minPixels > maxPixels {
				return out, errors.New("OCR pixel interval invalid")
			}
		}
	}
	optional, err := common.Marshal(opts)
	if err != nil {
		return out, err
	}
	data := in.File
	if in.FileURL != "" {
		data, err = s.fetch(ctx, in.FileURL, MaxInputBytes)
		if err != nil {
			return out, err
		}
	}
	if len(data) == 0 || len(data) > MaxInputBytes {
		return out, errors.New("OCR input byte bound exceeded")
	}
	out = validatedInput{data: append([]byte(nil), data...), pages: 1, optional: optional, urlMode: in.FileURL != ""}
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		conf := pdfmodel.NewStatelessConfiguration()
		conf.Offline = true
		conf.Limits = pdfmodel.ResourceLimits{MaxInputBytes: MaxInputBytes, MaxObjectBytes: 4 << 20, MaxStreamBytes: 20 << 20, MaxDecodeBytes: 32 << 20, MaxImagePixels: MaxImagePixels, MaxImageBytes: 100 << 20, MaxObjectCount: 100000, MaxObjectStreamCount: 10000, MaxObjectStreamFirst: 4 << 20, MaxXRefEntries: 100000, MaxRecursionDepth: 64}
		parseCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		out.pages, err = api.PageCount(parseCtx, bytes.NewReader(data), conf)
		if err != nil || out.pages < 1 || out.pages > MaxPages {
			return validatedInput{}, errors.New("OCR PDF invalid or page bound exceeded")
		}
		out.contentType = "application/pdf"
		out.filename = "document.pdf"
	} else {
		out.contentType, err = validateImage(data)
		if err != nil {
			return validatedInput{}, err
		}
		out.filename = "document.png"
		if out.contentType == "image/jpeg" {
			out.filename = "document.jpg"
		}
	}
	if in.Filename != "" {
		name := path.Base(strings.ReplaceAll(in.Filename, "\\", "/"))
		if len(name) > 200 || strings.ContainsAny(name, "\r\n\x00") {
			return validatedInput{}, errors.New("invalid filename")
		}
		out.filename = name
	}
	hash := sha256.New()
	hash.Write(data)
	hash.Write(optional)
	if out.urlMode {
		hash.Write([]byte("url"))
	}
	out.digest = hex.EncodeToString(hash.Sum(nil))
	return out, nil
}
func (s *Service) fetch(ctx context.Context, raw string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, errors.New("invalid OCR resource URL")
	}
	if req.URL.Scheme != "https" && req.URL.Scheme != "http" || req.URL.User != nil {
		return nil, errors.New("invalid OCR resource scheme")
	}
	resp, err := s.fetchClient.Do(req)
	if err != nil {
		return nil, errors.New("OCR protected download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > limit {
		return nil, errors.New("OCR download status or size invalid")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("OCR download byte bound exceeded")
	}
	return data, nil
}
