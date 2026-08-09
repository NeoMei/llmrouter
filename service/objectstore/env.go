package objectstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	defaultOnce  sync.Once
	defaultStore Store
	defaultErr   error
)

func Default() (Store, error) {
	defaultOnce.Do(func() {
		storeType := strings.ToLower(strings.TrimSpace(os.Getenv("OBJECT_STORE_TYPE")))
		switch storeType {
		case "", "local":
			root := strings.TrimSpace(os.Getenv("OBJECT_STORE_LOCAL_ROOT"))
			if root == "" {
				root = filepath.Join(os.TempDir(), "codex-gateway-objects")
			}
			defaultStore, defaultErr = NewLocal(root)
		case "s3":
			defaultStore, defaultErr = NewS3(S3Config{
				Endpoint:        os.Getenv("OBJECT_STORE_ENDPOINT"),
				Region:          os.Getenv("OBJECT_STORE_REGION"),
				Bucket:          os.Getenv("OBJECT_STORE_BUCKET"),
				AccessKeyID:     os.Getenv("OBJECT_STORE_ACCESS_KEY_ID"),
				SecretAccessKey: os.Getenv("OBJECT_STORE_SECRET_ACCESS_KEY"),
				UsePathStyle:    strings.EqualFold(os.Getenv("OBJECT_STORE_USE_PATH_STYLE"), "true"),
			})
		default:
			defaultErr = fmt.Errorf("unsupported OBJECT_STORE_TYPE %q", storeType)
		}
	})
	return defaultStore, defaultErr
}
