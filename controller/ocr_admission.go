package controller

import (
	"bytes"
	"context"
	"errors"
	"github.com/QuantumNous/new-api/service/ocr"
	"io"
	"mime"
	"net/http"
)

// ValidateManagedOCRBody reuses the native media validator before any hold.
func ValidateManagedOCRBody(ctx context.Context, ct string, body []byte) (string, error) {
	if OCRRuntime == nil {
		return "", errors.New("OCR unavailable")
	}
	kind, _, e := mime.ParseMediaType(ct)
	if e != nil || kind != "multipart/form-data" {
		return "", ocr.ErrInvalidInput
	}
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", bytes.NewReader(body))
	r.Header.Set("Content-Type", ct)
	if e = r.ParseMultipartForm(1 << 20); e != nil {
		return "", ocr.ErrInvalidInput
	}
	defer r.MultipartForm.RemoveAll()
	f := r.MultipartForm
	for name, values := range f.Value {
		if (name != "model" && name != "optionalPayload") || len(values) != 1 {
			return "", ocr.ErrInvalidInput
		}
	}
	if len(f.Value["model"]) != 1 || len(f.File) != 1 || len(f.File["file"]) != 1 {
		return "", ocr.ErrInvalidInput
	}
	file, header, e := r.FormFile("file")
	if e != nil {
		return "", e
	}
	defer file.Close()
	data, e := io.ReadAll(io.LimitReader(file, ocr.MaxInputBytes+1))
	if e != nil {
		return "", e
	}
	return r.FormValue("model"), OCRRuntime.Service.ValidateInput(ctx, ocr.Input{File: data, Filename: header.Filename, OptionalPayload: []byte(r.FormValue("optionalPayload"))})
}
