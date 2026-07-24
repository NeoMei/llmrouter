package profile

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/redis/go-redis/v9"
)

const (
	cacheKeyPrefix = "profile:model:"
	cacheTTL       = 5 * time.Minute
)

// DBQuerier abstracts the database lookup for a model's profile. The GORM-backed
// implementation lives with the model layer (provider_models + provider_profiles join).
type DBQuerier interface {
	QueryModelProfile(ctx context.Context, modelID string) (*ModelProfile, error)
}

// Loader resolves a ModelProfile with a Redis cache in front of the database.
// A nil rdb disables caching and always falls through to the DBQuerier.
type Loader struct {
	rdb *redis.Client
	db  DBQuerier
}

func NewLoader(rdb *redis.Client, db DBQuerier) *Loader {
	return &Loader{rdb: rdb, db: db}
}

// Get returns the profile for modelID, consulting Redis first and back-filling
// from the DB. Redis errors are non-fatal and degrade to a DB lookup.
func (l *Loader) Get(ctx context.Context, modelID string) (*ModelProfile, error) {
	key := cacheKeyPrefix + modelID

	if l.rdb != nil {
		if data, err := l.rdb.Get(ctx, key).Bytes(); err == nil {
			var p ModelProfile
			if common.Unmarshal(data, &p) == nil {
				return &p, nil
			}
		}
	}

	p, err := l.db.QueryModelProfile(ctx, modelID)
	if err != nil {
		return nil, fmt.Errorf("profile not found for model %s: %w", modelID, err)
	}

	if l.rdb != nil {
		if data, err := common.Marshal(p); err == nil {
			l.rdb.Set(ctx, key, data, cacheTTL)
		}
	}

	return p, nil
}

// Invalidate drops the cached profile for modelID. Call after an admin updates a profile.
func (l *Loader) Invalidate(ctx context.Context, modelID string) {
	if l.rdb != nil {
		l.rdb.Del(ctx, cacheKeyPrefix+modelID)
	}
}
