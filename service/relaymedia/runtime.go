package relaymedia

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/objectstore"
	"gorm.io/gorm"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Config is trusted process configuration. AllowLoopbackHTTP is for explicit
// local fixtures only; production publication origins require HTTPS.
type Config struct {
	PublicOrigin      string
	GrantSecret       []byte
	Objects           objectstore.Store
	AllowLoopbackHTTP bool
}

func NewService(db *gorm.DB, c Config) (*Service, error) {
	u, err := url.Parse(c.PublicOrigin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return nil, errors.New("RELAY_MEDIA_PUBLIC_ORIGIN must be a trusted HTTPS origin")
	}
	loopback := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && c.AllowLoopbackHTTP && loopback != nil && loopback.IsLoopback()) {
		return nil, errors.New("RELAY_MEDIA_PUBLIC_ORIGIN requires HTTPS (explicit loopback fixture exception only)")
	}
	if len(c.GrantSecret) < 32 || c.Objects == nil || db == nil {
		return nil, errors.New("relay media requires database, object store and independent secret of at least 32 bytes")
	}
	return &Service{db: db, objects: c.Objects, origin: strings.TrimRight(u.String(), "/"), secret: append([]byte(nil), c.GrantSecret...), now: time.Now}, nil
}

// Runtime has no billing connection or supplier replay. Close cancels and joins
// its bounded purge worker before the application's shared database is closed.
type Runtime struct {
	Service         *Service
	mu              sync.Mutex
	started, closed bool
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	once            sync.Once
}

func RuntimeFromEnv(db *gorm.DB) (*Runtime, error) {
	origin, secret := os.Getenv("RELAY_MEDIA_PUBLIC_ORIGIN"), os.Getenv("RELAY_MEDIA_GRANT_SECRET")
	if origin == "" && secret == "" {
		return nil, nil
	}
	if origin == "" || len(secret) < 32 {
		return nil, errors.New("RELAY_MEDIA_PUBLIC_ORIGIN and RELAY_MEDIA_GRANT_SECRET (at least 32 bytes) required")
	}
	objects, err := objectstore.Default()
	if err != nil {
		return nil, errors.New("relay media object store configuration invalid")
	}
	svc, err := NewService(db, Config{PublicOrigin: origin, GrantSecret: []byte(secret), Objects: objects, AllowLoopbackHTTP: os.Getenv("RELAY_MEDIA_ALLOW_LOOPBACK_HTTP") == "true"})
	if err != nil {
		return nil, err
	}
	r := &Runtime{Service: svc}
	r.Start()
	return r, nil
}
func (r *Runtime) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.closed || r.Service == nil {
		return
	}
	r.started = true
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			batch, stop := context.WithTimeout(ctx, 30*time.Second)
			err := r.Service.Purge(batch, 100)
			stop()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				common.SysError("relay media purge pending; tracked resources retained for retry")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (r *Runtime) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		if r.cancel != nil {
			r.cancel()
		}
		r.mu.Unlock()
		r.wg.Wait()
	})
}
