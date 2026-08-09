package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type LocalStore struct {
	root string
}

func NewLocal(root string) (*LocalStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("local object store root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, err
	}
	return &LocalStore{root: absolute}, nil
}

func (s *LocalStore) Put(ctx context.Context, key string, body io.Reader, opts PutOptions) (ObjectInfo, error) {
	target, err := s.resolve(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return ObjectInfo{}, err
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".upload-*")
	if err != nil {
		return ObjectInfo{}, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), body)
	closeErr := temp.Close()
	if copyErr != nil {
		return ObjectInfo{}, copyErr
	}
	if closeErr != nil {
		return ObjectInfo{}, closeErr
	}
	if opts.Size >= 0 && written != opts.Size {
		return ObjectInfo{}, fmt.Errorf("object size mismatch: declared %d, received %d", opts.Size, written)
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	if opts.ChecksumSHA256 != "" && !strings.EqualFold(opts.ChecksumSHA256, checksum) {
		return ObjectInfo{}, fmt.Errorf("object checksum mismatch")
	}
	if err := os.Rename(tempName, target); err != nil {
		return ObjectInfo{}, err
	}
	info := ObjectInfo{Key: key, ContentType: opts.ContentType, ETag: checksum, Size: written}
	metadata, err := json.Marshal(info)
	if err != nil {
		_ = os.Remove(target)
		return ObjectInfo{}, err
	}
	if err := os.WriteFile(target+".metadata.json", metadata, 0o640); err != nil {
		_ = os.Remove(target)
		return ObjectInfo{}, err
	}
	return info, nil
}

func (s *LocalStore) Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	target, err := s.resolve(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, ObjectInfo{}, err
	}
	metadata, err := os.ReadFile(target + ".metadata.json")
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	var info ObjectInfo
	if err := json.Unmarshal(metadata, &info); err != nil {
		return nil, ObjectInfo{}, err
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	return file, info, nil
}

func (s *LocalStore) Delete(ctx context.Context, key string) error {
	target, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, name := range []string{target, target + ".metadata.json"} {
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *LocalStore) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	probe, err := os.CreateTemp(s.root, ".health-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func (s *LocalStore) resolve(key string) (string, error) {
	if key == "" || strings.ContainsRune(key, '\x00') || strings.Contains(key, "\\") || path.IsAbs(key) {
		return "", fmt.Errorf("invalid object key")
	}
	clean := path.Clean(key)
	if clean != key || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid object key")
	}
	target := filepath.Join(s.root, filepath.FromSlash(clean))
	relative, err := filepath.Rel(s.root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid object key")
	}
	return target, nil
}
