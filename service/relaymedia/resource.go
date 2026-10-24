package relaymedia

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"github.com/QuantumNous/new-api/model"
	"strconv"
	"strings"
	"time"
)

func (s *Service) grant(r model.RelayMediaResource, expires int64) string {
	h := hmac.New(sha256.New, s.secret)
	fmt.Fprintf(h, "relay-media-v1\x00%s\x00%d\x00%d\x00%s", r.ID, expires, r.Size, r.SHA256)
	return strconv.FormatInt(expires, 10) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// ReadGranted verifies the ready row and capability, then returns an owned,
// bounded, hash-verified immutable buffer. No object-store path escapes. Already
// acquired buffers may finish streaming after revoke; later acquisitions fail.
func (s *Service) ReadGranted(ctx context.Context, id, token string) ([]byte, error) {
	if s == nil || len(id) != 36 || len(token) > 64 {
		return nil, ErrUnavailable
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, ErrUnavailable
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var data []byte
	err = model.ReadRelayMedia(ctx, s.db, id, func(r model.RelayMediaResource) error {
		now := s.now().Unix()
		if r.State != "ready" || r.ExpiresAt <= now || expires <= now || expires > r.ExpiresAt || expires > now+int64(GrantTTL/time.Second) || !hmac.Equal([]byte(s.grant(r, expires)), []byte(token)) {
			return ErrUnavailable
		}
		var e error
		data, e = s.readObject(ctx, r)
		return e
	})
	if err != nil {
		return nil, ErrUnavailable
	}
	return data, nil
}
