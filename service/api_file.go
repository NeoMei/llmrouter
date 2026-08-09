package service

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrAPIFileTooLarge = errors.New("file exceeds configured size limit")
	ErrAPIFileInvalid  = errors.New("invalid file")
	ErrAPIFileNotFound = errors.New("file not found")
)

type APIFileManager struct {
	Store    objectstore.Store
	MaxBytes int64
}

func DefaultAPIFileManager() (*APIFileManager, error) {
	store, err := objectstore.Default()
	if err != nil {
		return nil, err
	}
	maxBytes := int64(100 << 20)
	if raw := strings.TrimSpace(os.Getenv("API_FILE_MAX_BYTES")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("invalid API_FILE_MAX_BYTES")
		}
		maxBytes = parsed
	}
	return &APIFileManager{Store: store, MaxBytes: maxBytes}, nil
}

func (m *APIFileManager) Upload(ctx context.Context, tenantID string, userID int, reader *multipart.Reader) (*model.APIFile, error) {
	if m == nil || m.Store == nil || m.MaxBytes <= 0 {
		return nil, fmt.Errorf("file manager is not configured")
	}
	fileID := "file-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	storageKey := fmt.Sprintf("%s/%d/%s", safeKeySegment(tenantID), userID, fileID)
	var purpose, expectedChecksum, filename, mediaType string
	var byteCount int64
	var actualChecksum string
	stored := false

	cleanup := func() {
		if stored {
			_ = m.Store.Delete(context.Background(), storageKey)
		}
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanup()
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				return nil, ErrAPIFileTooLarge
			}
			return nil, fmt.Errorf("read multipart body: %w", err)
		}
		name := part.FormName()
		switch name {
		case "purpose":
			value, err := io.ReadAll(io.LimitReader(part, 1025))
			_ = part.Close()
			if err != nil || len(value) > 1024 {
				cleanup()
				return nil, fmt.Errorf("%w: invalid purpose", ErrAPIFileInvalid)
			}
			purpose = strings.TrimSpace(string(value))
		case "checksum_sha256":
			value, err := io.ReadAll(io.LimitReader(part, 65))
			_ = part.Close()
			if err != nil || len(value) > 64 {
				cleanup()
				return nil, fmt.Errorf("%w: invalid checksum", ErrAPIFileInvalid)
			}
			expectedChecksum = strings.ToLower(strings.TrimSpace(string(value)))
		case "file":
			if stored || part.FileName() == "" {
				_ = part.Close()
				cleanup()
				return nil, fmt.Errorf("%w: exactly one file is required", ErrAPIFileInvalid)
			}
			filename = filepath.Base(part.FileName())
			if filename != part.FileName() || !allowedAPIFileExtension(filename) {
				_ = part.Close()
				return nil, fmt.Errorf("%w: unsafe or unsupported filename", ErrAPIFileInvalid)
			}
			buffered := bufio.NewReader(part)
			header, _ := buffered.Peek(512)
			mediaType = http.DetectContentType(header)
			if !apiFileMediaMatchesExtension(filename, mediaType) {
				_ = part.Close()
				return nil, fmt.Errorf("%w: MIME type does not match extension", ErrAPIFileInvalid)
			}
			hash := sha256.New()
			counter := &countingReader{reader: io.LimitReader(buffered, m.MaxBytes+1)}
			_, err = m.Store.Put(ctx, storageKey, io.TeeReader(counter, hash), objectstore.PutOptions{
				ContentType: mediaType, Size: -1,
			})
			_ = part.Close()
			stored = err == nil
			if err != nil {
				cleanup()
				return nil, err
			}
			byteCount = counter.count
			actualChecksum = hex.EncodeToString(hash.Sum(nil))
			if byteCount > m.MaxBytes {
				cleanup()
				return nil, ErrAPIFileTooLarge
			}
		default:
			_ = part.Close()
		}
	}

	if !stored || purpose == "" {
		cleanup()
		return nil, fmt.Errorf("%w: file and purpose are required", ErrAPIFileInvalid)
	}
	if expectedChecksum != "" && expectedChecksum != actualChecksum {
		cleanup()
		return nil, fmt.Errorf("%w: checksum mismatch", ErrAPIFileInvalid)
	}
	file := &model.APIFile{
		ID: fileID, TenantID: tenantID, UserID: userID, Purpose: purpose, Filename: filename,
		Bytes: byteCount, ChecksumSHA256: actualChecksum, MediaType: mediaType,
		StorageKey: storageKey, Status: model.APIFileStatusReady,
	}
	if err := model.CreateAPIFile(file); err != nil {
		cleanup()
		return nil, err
	}
	return file, nil
}

func (m *APIFileManager) Open(ctx context.Context, tenantID string, userID int, id string) (io.ReadCloser, objectstore.ObjectInfo, *model.APIFile, error) {
	file, err := model.GetOwnedAPIFile(tenantID, userID, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, objectstore.ObjectInfo{}, nil, ErrAPIFileNotFound
	}
	if err != nil {
		return nil, objectstore.ObjectInfo{}, nil, err
	}
	body, info, err := m.Store.Open(ctx, file.StorageKey)
	return body, info, file, err
}

func (m *APIFileManager) Delete(ctx context.Context, tenantID string, userID int, id string) (bool, error) {
	file, err := model.GetOwnedAPIFile(tenantID, userID, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, ErrAPIFileNotFound
	}
	if err != nil {
		return false, err
	}
	if err := m.Store.Delete(ctx, file.StorageKey); err != nil {
		return false, err
	}
	return model.MarkAPIFileDeleted(tenantID, userID, id)
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	r.count += int64(n)
	return n, err
}

func safeKeySegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, value)
}

func allowedAPIFileExtension(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jsonl", ".json", ".txt", ".csv":
		return true
	default:
		return false
	}
}

func apiFileMediaMatchesExtension(filename, mediaType string) bool {
	extension := strings.ToLower(filepath.Ext(filename))
	mediaType = strings.ToLower(strings.Split(mediaType, ";")[0])
	switch extension {
	case ".jsonl", ".json":
		return mediaType == "application/json" || mediaType == "text/plain" || mediaType == "application/octet-stream"
	case ".txt", ".csv":
		return mediaType == "text/plain" || mediaType == "text/csv" || mediaType == "application/octet-stream"
	default:
		return false
	}
}
