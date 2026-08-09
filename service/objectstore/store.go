package objectstore

import (
	"context"
	"io"
)

type PutOptions struct {
	ContentType    string
	Size           int64
	ChecksumSHA256 string
}

type ObjectInfo struct {
	Key         string
	ContentType string
	ETag        string
	Size        int64
}

type Store interface {
	Put(ctx context.Context, key string, body io.Reader, opts PutOptions) (ObjectInfo, error)
	Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	Delete(ctx context.Context, key string) error
	Health(ctx context.Context) error
}
