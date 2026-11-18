package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/QuantumNous/new-api/service/ocr"
)

// ValidateManagedOCRBody reuses the native media validator before any hold.
func ValidateManagedOCRBody(ctx context.Context, ct string, body []byte) (string, error) {
	model, input, err := parseManagedOCRBody(ctx, ct, body)
	if err != nil {
		return "", err
	}
	return model, OCRRuntime.Service.ValidateInput(ctx, input)
}

// ManagedOCRRequestIdentity validates the bounded upload before admission and
// provides a stable call digest when a client rebuilds the multipart body.
func ManagedOCRRequestIdentity(ctx context.Context, ct string, body []byte) (string, []byte, error) {
	model, input, err := parseManagedOCRBody(ctx, ct, body)
	if err != nil {
		return "", nil, err
	}
	identity, err := OCRRuntime.Service.ManagedInputIdentity(ctx, input)
	return model, identity, err
}

// OCRMultipartBodyLimit leaves room for the declared document options and MIME framing.
const OCRMultipartBodyLimit = ocr.MaxInputBytes + (64 << 10)

func parseManagedOCRBody(ctx context.Context, ct string, body []byte) (string, ocr.Input, error) {
	if OCRRuntime == nil || OCRRuntime.Service == nil {
		return "", ocr.Input{}, errors.New("OCR unavailable")
	}
	if len(body) > OCRMultipartBodyLimit {
		return "", ocr.Input{}, ocr.ErrInvalidInput
	}
	kind, _, e := mime.ParseMediaType(ct)
	if e != nil || kind != "multipart/form-data" {
		return "", ocr.Input{}, ocr.ErrInvalidInput
	}
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", bytes.NewReader(body))
	r.Header.Set("Content-Type", ct)
	if e = r.ParseMultipartForm(1 << 20); e != nil {
		return "", ocr.Input{}, ocr.ErrInvalidInput
	}
	defer r.MultipartForm.RemoveAll()
	f := r.MultipartForm
	for name, values := range f.Value {
		if (name != "model" && name != "optionalPayload") || len(values) != 1 {
			return "", ocr.Input{}, ocr.ErrInvalidInput
		}
	}
	if len(f.Value["model"]) != 1 || len(f.File) != 1 || len(f.File["file"]) != 1 {
		return "", ocr.Input{}, ocr.ErrInvalidInput
	}
	file, header, e := r.FormFile("file")
	if e != nil {
		return "", ocr.Input{}, e
	}
	defer file.Close()
	data, e := io.ReadAll(io.LimitReader(file, ocr.MaxInputBytes+1))
	if e != nil {
		return "", ocr.Input{}, e
	}
	return f.Value["model"][0], ocr.Input{File: data, Filename: header.Filename, OptionalPayload: []byte(r.FormValue("optionalPayload"))}, nil
}
