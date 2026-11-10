package controller

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/paddleocr"
	"github.com/QuantumNous/new-api/service"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	paddleOCRProbeDefaultOrigin = "https://paddleocr.aistudio-app.com"
	paddleOCRProbeOperation     = "ocr.jobs.create"
	paddleOCRProbePollInterval  = 1500 * time.Millisecond
)

// paddleOCRProbeURL mirrors the OCR relay target: custom base URLs are allowed,
// and the official origin is used when the channel leaves it empty.
func paddleOCRProbeURL(channel *model.Channel) string {
	base := ""
	if channel != nil {
		base = strings.TrimRight(strings.TrimSpace(channel.GetBaseURL()), "/")
	}
	if base == "" {
		base = paddleOCRProbeDefaultOrigin
	}
	if !strings.HasSuffix(base, "/api/v2/ocr/jobs") {
		base += "/api/v2/ocr/jobs"
	}
	return base
}

// paddleOCRProbeImage renders a small text page so the supplier has real input
// to extract. A blank page would make "no text" indistinguishable from a failure.
func paddleOCRProbeImage() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 240, 64))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	drawer := &font.Drawer{Dst: img, Src: image.Black, Face: basicfont.Face7x13, Dot: fixed.P(12, 40)}
	drawer.DrawString("LLMROUTER TEST 123")
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func paddleOCRProbeModel(channel *model.Channel, testModel string) string {
	if value := strings.TrimSpace(testModel); value != "" {
		return value
	}
	if channel != nil && channel.TestModel != nil {
		if value := strings.TrimSpace(*channel.TestModel); value != "" {
			return value
		}
	}
	if channel != nil {
		for _, value := range channel.GetModels() {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

// paddleOCRProbeKey takes the first pool-admitted key. A manual connection test
// must not advance multi-key rotation or record supplier health.
func paddleOCRProbeKey(ctx context.Context, channel *model.Channel, modelName string) (string, error) {
	refs, keys, err := model.ChannelAccountRefs(ctx, model.DB, channel)
	if err != nil {
		return "", err
	}
	if len(refs) == 0 {
		return "", fmt.Errorf("PaddleOCR channel has no available key")
	}
	ref := refs[0]
	if ref.KeyIndex < 0 || ref.KeyIndex >= len(keys) || strings.TrimSpace(keys[ref.KeyIndex]) == "" {
		return "", fmt.Errorf("PaddleOCR channel key %d is unavailable", ref.KeyIndex)
	}
	// ponytail: first admitted key only; rotate through refs if a single key's
	// cooldown makes manual tests misleading.
	if err := model.CheckUpstreamAccounts(ctx, model.DB, []model.UpstreamAccountRef{ref},
		model.UpstreamAccountScope{Operation: paddleOCRProbeOperation, Model: modelName}, time.Now()); err != nil {
		return "", fmt.Errorf("PaddleOCR channel key is cooling down: %v", err)
	}
	return keys[ref.KeyIndex], nil
}

// testPaddleOCRChannel submits one tiny OCR job and polls it. It never bills and
// never writes channel health, so the generic chat probe stays out of this path.
func testPaddleOCRChannel(ctx context.Context, channel *model.Channel, testModel string, budget time.Duration) testResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if channel == nil {
		return testResult{localErr: fmt.Errorf("PaddleOCR channel is missing")}
	}
	modelName := paddleOCRProbeModel(channel, testModel)
	if modelName == "" {
		return testResult{localErr: fmt.Errorf("PaddleOCR channel has no model to test")}
	}
	key, err := paddleOCRProbeKey(ctx, channel, modelName)
	if err != nil {
		return testResult{localErr: err}
	}
	page, err := paddleOCRProbeImage()
	if err != nil {
		return testResult{localErr: fmt.Errorf("build PaddleOCR probe page: %w", err)}
	}
	jobsURL := paddleOCRProbeURL(channel)
	client := paddleocr.Client{HTTP: service.GetHttpClient()}
	jobID, submit, err := client.Submit(ctx, jobsURL, key, paddleocr.Input{Model: modelName, Filename: "probe.png", File: page})
	if err != nil {
		if submit.Status != 0 {
			return testResult{localErr: fmt.Errorf("PaddleOCR submit failed: HTTP %d", submit.Status)}
		}
		return testResult{localErr: fmt.Errorf("PaddleOCR submit failed: %v", err)}
	}
	if budget <= 0 {
		budget = 45 * time.Second
	}
	deadline := time.Now().Add(budget)
	for {
		select {
		case <-ctx.Done():
			return testResult{localErr: fmt.Errorf("PaddleOCR probe cancelled: %v", ctx.Err())}
		case <-time.After(paddleOCRProbePollInterval):
		}
		status, response, err := client.Status(ctx, jobsURL, key, jobID)
		if err != nil {
			if response.Status != 0 {
				return testResult{localErr: fmt.Errorf("PaddleOCR status failed: HTTP %d", response.Status)}
			}
			return testResult{localErr: fmt.Errorf("PaddleOCR status failed: %v", err)}
		}
		switch status.State {
		case "success", "succeeded", "completed", "finished", "done":
			return testResult{}
		case "failed", "error", "cancelled", "canceled", "expired":
			return testResult{localErr: fmt.Errorf("PaddleOCR job %s ended in state %q", jobID, status.State)}
		}
		if time.Now().After(deadline) {
			return testResult{localErr: fmt.Errorf("PaddleOCR job %s did not finish within %s (state %q)", jobID, budget, status.State)}
		}
	}
}
