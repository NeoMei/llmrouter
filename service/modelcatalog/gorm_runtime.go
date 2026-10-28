package modelcatalog

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"gorm.io/gorm"
)

// GormRuntimeSources projects the existing channel, ability and pricing
// stores into the RuntimeSources boundary. It never selects or returns channel
// credentials.
type GormRuntimeSources struct {
	DB *gorm.DB
}

func NewGormRuntimeSources(db *gorm.DB) GormRuntimeSources { return GormRuntimeSources{DB: db} }

func (s GormRuntimeSources) Channel(ctx context.Context, id int) (RuntimeChannel, bool, error) {
	if s.DB == nil {
		return RuntimeChannel{}, false, gorm.ErrInvalidDB
	}
	var channel model.Channel
	if err := s.DB.WithContext(ctx).First(&channel, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return RuntimeChannel{}, false, nil
		}
		return RuntimeChannel{}, false, err
	}
	return RuntimeChannel{Type: channel.Type, ID: channel.Id, Enabled: channel.Status == common.ChannelStatusEnabled, ModelMapping: channel.GetModelMapping()}, true, nil
}

func (s GormRuntimeSources) Abilities(ctx context.Context, channelID int, upstream string) ([]RuntimeAbility, error) {
	if s.DB == nil {
		return nil, gorm.ErrInvalidDB
	}
	var abilities []model.Ability
	if err := s.DB.WithContext(ctx).
		Where("channel_id = ? AND enabled = ? AND model = ?", channelID, true, upstream).
		Order("priority DESC").Order("weight DESC").Find(&abilities).Error; err != nil {
		return nil, err
	}
	result := make([]RuntimeAbility, 0, len(abilities))
	for _, ability := range abilities {
		priority := int64(0)
		if ability.Priority != nil {
			priority = *ability.Priority
		}
		result = append(result, RuntimeAbility{Group: ability.Group, Model: ability.Model, Enabled: ability.Enabled, Priority: priority, Weight: ability.Weight})
	}
	return result, nil
}

func (s GormRuntimeSources) CanonicalBillingModel(_ context.Context, modelName string) (string, bool, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return "", false, nil
	}
	if helper.HasModelBillingConfig(modelName) {
		return modelName, true, nil
	}
	// Pricing metadata may canonicalize a case/pattern alias; only return a
	// candidate that is also accepted by the existing billing helper.
	for _, pricing := range model.GetPricing() {
		if strings.EqualFold(strings.TrimSpace(pricing.ModelName), modelName) && helper.HasModelBillingConfig(pricing.ModelName) {
			return pricing.ModelName, true, nil
		}
	}
	return "", false, nil
}

// RecognizesPublicModel is only a distributor deferral hint. It grants no
// visibility, channel or group authority; the controller must resolve again.
func RecognizesPublicModel(ctx context.Context, db *gorm.DB, requested string) (bool, error) {
	if db == nil || !db.Migrator().HasTable(&model.ClientModelRoute{}) || !db.Migrator().HasTable(&model.ClientModelTarget{}) {
		return false, nil
	}
	var routes []model.ClientModelRoute
	if err := db.WithContext(ctx).Select("public_id", "aliases").Where("enabled = ?", true).Find(&routes).Error; err != nil {
		return false, err
	}
	for _, route := range routes {
		if strings.EqualFold(strings.TrimSpace(route.PublicId), strings.TrimSpace(requested)) {
			return true, nil
		}
		aliases, err := decodeStrings(route.Aliases)
		if err != nil {
			return false, err
		}
		for _, alias := range aliases {
			if strings.EqualFold(strings.TrimSpace(alias), strings.TrimSpace(requested)) {
				return true, nil
			}
		}
	}
	return false, nil
}

func PublicModelSupportsOperation(ctx context.Context, db *gorm.DB, requested, operation string) (bool, error) {
	if db == nil || !db.Migrator().HasTable(&model.ClientModelRoute{}) || !db.Migrator().HasTable(&model.ClientModelTarget{}) {
		return false, nil
	}
	var routes []model.ClientModelRoute
	if err := db.WithContext(ctx).Select("public_id", "aliases", "metadata").Where("enabled = ?", true).Find(&routes).Error; err != nil {
		return false, err
	}
	for _, route := range routes {
		matches := strings.EqualFold(strings.TrimSpace(route.PublicId), strings.TrimSpace(requested))
		if !matches {
			aliases, err := decodeStrings(route.Aliases)
			if err != nil {
				return false, err
			}
			for _, alias := range aliases {
				matches = matches || strings.EqualFold(strings.TrimSpace(alias), strings.TrimSpace(requested))
			}
		}
		if !matches {
			continue
		}
		var metadata struct {
			Operations []string `json:"operations"`
		}
		if common.Unmarshal([]byte(route.Metadata), &metadata) != nil {
			return false, nil
		}
		for _, candidate := range metadata.Operations {
			if candidate == operation {
				return true, nil
			}
		}
		return false, nil
	}
	return false, nil
}
