package model

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/profile"
)

// GormProfileQuerier implements profile.DBQuerier using the GORM DB.
type GormProfileQuerier struct{}

func NewGormProfileQuerier() *GormProfileQuerier { return &GormProfileQuerier{} }

func (q *GormProfileQuerier) QueryModelProfile(ctx context.Context, modelID string) (*profile.ModelProfile, error) {
	var pm ProviderModel
	err := DB.WithContext(ctx).
		Where("model_id = ? AND status = 'active'", modelID).
		First(&pm).Error
	if err != nil {
		return nil, err
	}

	var pp ProviderProfile
	err = DB.WithContext(ctx).
		Where("id = ? AND status = 'active'", pm.ProfileId).
		First(&pp).Error
	if err != nil {
		return nil, err
	}

	var rules profile.QuirkRules
	if pm.QuirkRules != "" && pm.QuirkRules != "{}" {
		if err := common.Unmarshal([]byte(pm.QuirkRules), &rules); err != nil {
			rules = profile.DefaultQuirkRules()
		}
	} else {
		rules = profile.DefaultQuirkRules()
	}

	var inputModes []string
	if pm.InputModes != "" {
		_ = common.Unmarshal([]byte(pm.InputModes), &inputModes)
	}
	if len(inputModes) == 0 {
		inputModes = []string{"text"}
	}

	maxOutput := pm.MaxOutput
	if maxOutput <= 0 {
		maxOutput = 8192
	}
	contextWindow := pm.ContextWindow
	if contextWindow <= 0 {
		contextWindow = 131072
	}

	return &profile.ModelProfile{
		ModelID:       pm.ModelId,
		DisplayName:   pm.DisplayName,
		ContextWindow: contextWindow,
		MaxOutput:     maxOutput,
		InputModes:    inputModes,
		QuirkRules:    rules,
		ProfileID:     pp.Id,
		ProfileName:   pp.Name,
		BaseURL:       pp.BaseURL,
		AuthMode:      pp.AuthMode,
	}, nil
}

// GetModelProfileByModelId is a convenience wrapper for non-profile consumers.
func GetModelProfileByModelId(modelID string) (*profile.ModelProfile, error) {
	return NewGormProfileQuerier().QueryModelProfile(context.Background(), modelID)
}

// ProviderModelCount returns the total number of active provider models.
func ProviderModelCount() (int64, error) {
	var count int64
	err := DB.Model(&ProviderModel{}).Where("status = 'active'").Count(&count).Error
	return count, err
}

// EnsureTime is a helper for seed/migration code.
func EnsureTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
