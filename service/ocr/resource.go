package ocr

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"

	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/google/uuid"
)

func (s *Service) grant(r model.OCRResource, expires int64) string {
	h := hmac.New(sha256.New, s.grantSecret)
	fmt.Fprintf(h, "ocr-resource-v1\x00%s\x00%s\x00%d", r.JobID, r.ID, expires)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Service) resourceURL(ctx context.Context, r model.OCRResource) (string, error) {
	now := s.now().Unix()
	if r.Revoked || r.ExpiresAt <= now {
		return "", model.ErrOCRGrant
	}
	seconds := int64(s.grantTTL / time.Second)
	bucket := max(seconds/2, 1)
	expires := min((now/bucket)*bucket+seconds, r.ExpiresAt)
	token := s.grant(r, expires)
	digest := sha256.Sum256([]byte(token))
	err := s.billing.PutOCRGrant(ctx, model.OCRResourceGrant{ResourceID: r.ID, JobID: r.JobID, ExpiresAt: expires, Digest: hex.EncodeToString(digest[:])}, now)
	if err != nil {
		return "", err
	}
	return s.publicOrigin + "/api/ocr/jobs/" + url.PathEscape(r.JobID) + "/resources/" + url.PathEscape(r.ID) + "?token=" + url.QueryEscape(token), nil
}
func (s *Service) storeResource(ctx context.Context, jobID, kind, contentType string, data []byte, expires int64) (model.OCRResource, error) {
	r := model.OCRResource{ID: uuid.NewString(), JobID: jobID, Kind: kind, ContentType: contentType, Size: int64(len(data)), ExpiresAt: expires}
	r.ObjectKey = "ocr/" + jobID + "/" + r.ID
	sum := sha256.Sum256(data)
	r.Checksum = hex.EncodeToString(sum[:])
	_, err := s.objects.Put(ctx, r.ObjectKey, bytes.NewReader(data), objectstore.PutOptions{ContentType: contentType, Size: r.Size, ChecksumSHA256: r.Checksum})
	return r, err
}

// OpenGrantedResource requires only the job/resource-specific bearer capability,
// never an upstream token. The HTTP adapter must not expose ObjectKey metadata.
func (s *Service) OpenGrantedResource(ctx context.Context, jobID, resourceID, grant string) (io.ReadCloser, ResourceInfo, error) {
	var info ResourceInfo
	if len(grant) != 43 {
		return nil, info, model.ErrOCRGrant
	}
	r, err := s.billing.OCRResource(ctx, jobID, resourceID)
	if err != nil {
		return nil, info, model.ErrOCRGrant
	}
	digest := sha256.Sum256([]byte(grant))
	if r.Revoked || r.ExpiresAt <= s.now().Unix() {
		return nil, info, model.ErrOCRGrant
	}
	if err = s.billing.ValidateOCRGrant(ctx, jobID, resourceID, hex.EncodeToString(digest[:]), s.now().Unix()); err != nil {
		return nil, info, model.ErrOCRGrant
	}
	body, stored, err := s.objects.Open(ctx, r.ObjectKey)
	if err != nil {
		return nil, info, err
	}
	if stored.Size != r.Size {
		body.Close()
		return nil, info, errors.New("OCR stored resource size mismatch")
	}
	if r.Kind == "jsonl" {
		data, readErr := io.ReadAll(io.LimitReader(body, MaxResultBytes+1))
		body.Close()
		if readErr != nil || len(data) > MaxResultBytes {
			return nil, info, errors.New("invalid stored OCR JSONL")
		}
		fresh, refreshErr := s.refreshResultGrants(ctx, jobID, data)
		if refreshErr != nil {
			return nil, info, refreshErr
		}
		return io.NopCloser(bytes.NewReader(fresh)), ResourceInfo{ContentType: r.ContentType, Size: int64(len(fresh)), ExpiresAt: r.ExpiresAt}, nil
	}
	return body, ResourceInfo{ContentType: r.ContentType, Size: r.Size, ExpiresAt: r.ExpiresAt}, nil
}

type ResourceInfo struct {
	ContentType     string
	Size, ExpiresAt int64
}

func (s *Service) PurgeExpiredResources(ctx context.Context, limit int) error {
	return s.billing.PurgeOCRResources(ctx, limit, s.now().Unix(), s.objects.Delete)
}

// preservePage rewrites only markdown.images URL values; raw JSON fields at all
// other levels survive, including tables, formulas and unknown native layout.
func (s *Service) preserveResults(ctx context.Context, j model.OCRJob) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var execution model.OCRExecution
	if err := common.UnmarshalJsonStr(j.Execution, &execution); err != nil {
		return err
	}
	data, err := s.fetch(ctx, j.ResultURL, MaxResultBytes)
	if err != nil {
		return err
	}
	resources := []model.OCRResource{}
	published := false
	defer func() {
		if !published {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			for _, r := range resources {
				if cleanupCtx.Err() != nil {
					break
				}
				_ = s.objects.Delete(cleanupCtx, r.ObjectKey)
			}
		}
	}()
	expires := s.now().Add(s.retention).Unix()
	images := map[string]string{}
	totalBytes := len(data)
	pages := 0
	var output bytes.Buffer
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var root map[string]json.RawMessage
		if err = common.Unmarshal(line, &root); err != nil || root == nil {
			return errors.New("invalid OCR JSONL")
		}
		var result map[string]json.RawMessage
		if err = common.Unmarshal(root["result"], &result); err != nil || result == nil {
			return errors.New("missing OCR native result")
		}
		var layouts []json.RawMessage
		if err = common.Unmarshal(result["layoutParsingResults"], &layouts); err != nil || len(layouts) == 0 {
			return errors.New("missing OCR native pages")
		}
		for i, page := range layouts {
			pages++
			if pages > MaxPages {
				return errors.New("OCR result page bound exceeded")
			}
			var pageObject map[string]json.RawMessage
			if err = common.Unmarshal(page, &pageObject); err != nil || pageObject == nil {
				return errors.New("invalid OCR page")
			}
			if raw, ok := pageObject["markdown"]; ok {
				var markdown map[string]json.RawMessage
				if err = common.Unmarshal(raw, &markdown); err != nil || markdown == nil {
					return errors.New("invalid OCR markdown")
				}
				if imageRaw, ok := markdown["images"]; ok && string(imageRaw) != "null" {
					var refs map[string]string
					if err = common.Unmarshal(imageRaw, &refs); err != nil {
						return errors.New("invalid OCR image references")
					}
					for name, upstream := range refs {
						if upstream == "" {
							return errors.New("empty OCR image URL")
						}
						owned, ok := images[upstream]
						if !ok {
							if len(images) >= MaxImages {
								return errors.New("OCR image count bound exceeded")
							}
							bytes, err := s.fetch(ctx, upstream, MaxImageBytes)
							if err != nil {
								return err
							}
							totalBytes += len(bytes)
							if totalBytes > MaxTotalResultBytes {
								return errors.New("OCR aggregate result bound exceeded")
							}
							mime, err := validateImage(bytes)
							if err != nil {
								return err
							}
							r, err := s.storeResource(ctx, j.ID, "image", mime, bytes, expires)
							if err != nil {
								return err
							}
							resources = append(resources, r)
							owned = "ocr-resource://" + r.ID
							images[upstream] = owned
						}
						refs[name] = owned
					}
					markdown["images"], err = common.Marshal(refs)
					if err != nil {
						return err
					}
				}
				pageObject["markdown"], err = common.Marshal(markdown)
				if err != nil {
					return err
				}
			}
			layouts[i], err = common.Marshal(pageObject)
			if err != nil {
				return err
			}
		}
		result["layoutParsingResults"], err = common.Marshal(layouts)
		if err != nil {
			return err
		}
		root["result"], err = common.Marshal(result)
		if err != nil {
			return err
		}
		encoded, err := common.Marshal(root)
		if err != nil {
			return err
		}
		output.Write(encoded)
		output.WriteByte('\n')
		if output.Len() > MaxResultBytes {
			return errors.New("OCR rewritten result byte bound exceeded")
		}
	}
	if pages == 0 || pages != execution.InputPages || (j.TotalPages > 0 && pages != j.TotalPages) {
		return errors.New("OCR result page count mismatch")
	}
	if totalBytes-len(data)+output.Len() > MaxTotalResultBytes {
		return errors.New("OCR rewritten aggregate bound exceeded")
	}
	r, err := s.storeResource(ctx, j.ID, "jsonl", "application/x-ndjson", output.Bytes(), expires)
	if err != nil {
		return err
	}
	resources = append(resources, r)
	published, err = s.billing.PublishOCRResources(ctx, j.ID, resources, r.ID)
	if err != nil {
		published = true
	} // Commit acknowledgement may be lost: never delete possibly published objects.
	return err
}

// Only a trusted configured origin is used; request Host is never consulted.
func validateOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("OCR trusted public origin required")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// Stored JSONL is a lossless native layout template. Only its image grant URL
// values are renewed on read, so neither provider downloads nor OCR/billing is
// repeated when the short-lived download capabilities expire.
func (s *Service) refreshResultGrants(ctx context.Context, jobID string, data []byte) ([]byte, error) {
	var output bytes.Buffer
	renewed := map[string]string{}
	var imageBytes int64
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var root, result map[string]json.RawMessage
		if err := common.Unmarshal(line, &root); err != nil {
			return nil, err
		}
		if err := common.Unmarshal(root["result"], &result); err != nil {
			return nil, err
		}
		var pages []json.RawMessage
		if err := common.Unmarshal(result["layoutParsingResults"], &pages); err != nil {
			return nil, err
		}
		for i, raw := range pages {
			var page map[string]json.RawMessage
			if err := common.Unmarshal(raw, &page); err != nil {
				return nil, err
			}
			markdownRaw, ok := page["markdown"]
			if !ok {
				continue
			}
			var markdown map[string]json.RawMessage
			if err := common.Unmarshal(markdownRaw, &markdown); err != nil {
				return nil, err
			}
			imagesRaw, ok := markdown["images"]
			if !ok || string(imagesRaw) == "null" {
				continue
			}
			var images map[string]string
			if err := common.Unmarshal(imagesRaw, &images); err != nil {
				return nil, err
			}
			for name, oldURL := range images {
				fresh, ok := renewed[oldURL]
				if !ok {
					u, err := url.Parse(oldURL)
					if err != nil || u.Scheme != "ocr-resource" || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
						return nil, model.ErrOCRGrant
					}
					resource, err := s.billing.OCRResource(ctx, jobID, u.Host)
					if err != nil || resource.Kind != "image" {
						return nil, model.ErrOCRGrant
					}
					imageBytes += resource.Size
					if imageBytes > MaxTotalResultBytes {
						return nil, errors.New("OCR aggregate resource bound exceeded")
					}
					fresh, err = s.resourceURL(ctx, resource)
					if err != nil {
						return nil, err
					}
					renewed[oldURL] = fresh
				}
				images[name] = fresh
			}
			encoded, err := common.Marshal(images)
			if err != nil {
				return nil, err
			}
			markdown["images"] = encoded
			encoded, err = common.Marshal(markdown)
			if err != nil {
				return nil, err
			}
			page["markdown"] = encoded
			pages[i], err = common.Marshal(page)
			if err != nil {
				return nil, err
			}
		}
		encoded, err := common.Marshal(pages)
		if err != nil {
			return nil, err
		}
		result["layoutParsingResults"] = encoded
		encoded, err = common.Marshal(result)
		if err != nil {
			return nil, err
		}
		root["result"] = encoded
		encoded, err = common.Marshal(root)
		if err != nil {
			return nil, err
		}
		output.Write(encoded)
		output.WriteByte('\n')
		if output.Len() > MaxResultBytes {
			return nil, errors.New("OCR result byte bound exceeded")
		}
	}
	if imageBytes+int64(output.Len()) > MaxTotalResultBytes {
		return nil, errors.New("OCR projected aggregate result bound exceeded")
	}
	return output.Bytes(), nil
}
