// Package paddleocr implements the hosted v2 jobs wire protocol. Account
// selection, health, financial decisions and downloads belong to the owner.
package paddleocr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

type Input struct {
	Model    string
	Filename string
	File     []byte
	// FileURL must be a service-owned grant to already validated immutable bytes.
	FileURL         string
	OptionalPayload json.RawMessage
}
type Status struct {
	JobID           string `json:"jobId"`
	State           string `json:"state"`
	ExtractProgress struct {
		TotalPages     int `json:"totalPages"`
		ExtractedPages int `json:"extractedPages"`
	} `json:"extractProgress"`
	ResultURL struct {
		JSONURL string `json:"jsonUrl"`
	} `json:"resultUrl"`
}
type Response struct {
	Status int
	Header http.Header
	Data   json.RawMessage
	Code   int
}

func (r Response) Refused() bool { return r.Status == 401 || r.Status == 429 }
func (r Response) RetryVeto() bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Should-Retry")), "false")
}

// Doer must not retry POST or follow redirects with credentials.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}
type Client struct{ HTTP Doer }

func (c Client) Submit(ctx context.Context, jobsURL, key string, in Input) (string, Response, error) {
	var body bytes.Buffer
	contentType := "application/json"
	if in.FileURL != "" {
		raw, err := common.Marshal(struct {
			FileURL  string          `json:"fileUrl"`
			Model    string          `json:"model"`
			Optional json.RawMessage `json:"optionalPayload"`
		}{in.FileURL, in.Model, in.OptionalPayload})
		if err != nil {
			return "", Response{}, err
		}
		body.Write(raw)
	} else {
		w := multipart.NewWriter(&body)
		if err := w.WriteField("model", in.Model); err != nil {
			return "", Response{}, err
		}
		if err := w.WriteField("optionalPayload", string(in.OptionalPayload)); err != nil {
			return "", Response{}, err
		}
		part, err := w.CreateFormFile("file", in.Filename)
		if err != nil {
			return "", Response{}, err
		}
		if _, err = part.Write(in.File); err != nil {
			return "", Response{}, err
		}
		if err = w.Close(); err != nil {
			return "", Response{}, err
		}
		contentType = w.FormDataContentType()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, jobsURL, &body)
	if err != nil {
		return "", Response{}, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+key)
	r, err := c.do(req)
	if err != nil {
		return "", r, err
	}
	var data struct {
		JobID string `json:"jobId"`
	}
	if err = common.UnmarshalUnique(r.Data, &data); err != nil || data.JobID == "" || len(data.JobID) > 256 {
		return "", r, errors.New("invalid PaddleOCR job response")
	}
	return data.JobID, r, nil
}
func (c Client) Status(ctx context.Context, jobsURL, key, id string) (Status, Response, error) {
	var status Status
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(jobsURL, "/")+"/"+url.PathEscape(id), nil)
	if err != nil {
		return status, Response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	r, err := c.do(req)
	if err != nil {
		return status, r, err
	}
	if err = common.UnmarshalUnique(r.Data, &status); err != nil {
		return status, r, errors.New("invalid PaddleOCR status")
	}
	status.State = strings.ToLower(status.State)
	if status.State == "" {
		return status, r, errors.New("missing PaddleOCR state")
	}
	return status, r, nil
}
func (c Client) do(req *http.Request) (Response, error) {
	var out Response
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return out, errors.New("PaddleOCR transport outcome unknown")
	}
	defer resp.Body.Close()
	out.Status, out.Header = resp.StatusCode, resp.Header.Clone()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return out, errors.New("invalid PaddleOCR response size")
	}
	if resp.StatusCode != 200 {
		return out, errors.New("PaddleOCR HTTP refusal or failure")
	}
	var envelope struct {
		Code *int            `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := common.UnmarshalUnique(raw, &envelope); err != nil || envelope.Code == nil || *envelope.Code != 0 || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		if envelope.Code != nil {
			out.Code = *envelope.Code
		}
		return out, errors.New("PaddleOCR rejected or malformed response")
	}
	out.Data = envelope.Data
	return out, nil
}
