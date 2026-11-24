package modelcatalog

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
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
	if err := s.DB.WithContext(ctx).Select("id", "type", "status", "model_mapping").First(&channel, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return RuntimeChannel{}, false, nil
		}
		return RuntimeChannel{}, false, err
	}
	return RuntimeChannel{Type: channel.Type, ID: channel.Id, Enabled: channel.Status == common.ChannelStatusEnabled, ModelMapping: channel.GetModelMapping()}, true, nil
}

func (s GormRuntimeSources) DisabledModels(ctx context.Context) (map[string]bool, error) {
	return model.DisabledModelNames(ctx, s.DB)
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

// ValidateTargetPricing checks the pricing contract of Ark operations whose
// relay path needs a specific billing mode. Managed external tariffs are
// certified independently by the platform billing path.
func (GormRuntimeSources) ValidateTargetPricing(channelType int, upstream, billingModel string, operations []string) error {
	for _, operation := range operations {
		switch {
		case channelType == constant.ChannelTypeVolcEngine && operation == string(capability.OperationImageGeneration):
			if _, ok := helper.ArkImageTariffForModel(upstream); !ok {
				return fmt.Errorf("Ark image model has no verified per-image price")
			}
			price, priced := ratio_setting.GetModelPrice(billingModel, false)
			if !priced || !validPrice(price) || billing_setting.GetBillingMode(billingModel) == billing_setting.BillingModeTieredExpr {
				return fmt.Errorf("Ark image models require per-image ModelPrice configuration")
			}
		case channelType == constant.ChannelTypeDoubaoVideo && operation == string(capability.OperationVideoCreate):
			if _, ok := billing_setting.ResolveModelNativePrice(billingModel, "video"); ok {
				continue
			}
			if price, ok := ratio_setting.GetModelPrice(billingModel, false); ok && validPrice(price) {
				continue
			}
			if price, ok := ratio_setting.GetDefaultModelPriceMap()[billingModel]; ok && validPrice(price) {
				continue
			}
			if ratio, ok, _ := ratio_setting.GetModelRatio(billingModel); ok && validPrice(ratio) {
				continue
			}
			return fmt.Errorf("Ark video models require per-call or token-usage pricing")
		}
	}
	return nil
}

func validPrice(price float64) bool {
	return price >= 0 && !math.IsNaN(price) && !math.IsInf(price, 0)
}

func (s GormRuntimeSources) CanonicalBillingModel(_ context.Context, modelName string) (string, bool, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return "", false, nil
	}
	if helper.HasModelBillingConfig(modelName) || billing_setting.HasModelNativePricing(modelName) {
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

// RequestClient supplies the defaults shared by discovery and relay entry points.
func RequestClient(explicit, path string) string {
	if client := strings.ToLower(strings.TrimSpace(explicit)); client != "" {
		return client
	}
	if strings.Contains(path, "/v1/messages") {
		return "claude"
	}
	if strings.Contains(path, "/v1beta/models") || (strings.Contains(path, "/v1/models/") && strings.Contains(path, ":")) {
		return "gemini"
	}
	return "codex"
}

func RouteMatchesClient(routeClient, client string) bool {
	routeClient = strings.ToLower(strings.TrimSpace(routeClient))
	client = strings.ToLower(strings.TrimSpace(client))
	return client == "" || routeClient == "" || routeClient == "all" || routeClient == client
}

// RecognizesPublicModel is a deferral hint. Runtime still validates the target,
// group, and pricing before any upstream dispatch.
func RecognizesPublicModel(ctx context.Context, db *gorm.DB, requested string, client ...string) (bool, error) {
	route, err := findPublicModelRoute(ctx, db, requested, client)
	return route != nil, err
}

func PublicModelSupportsOperation(ctx context.Context, db *gorm.DB, requested, operation string, client ...string) (bool, error) {
	route, err := findPublicModelRoute(ctx, db, requested, client)
	if route == nil || err != nil || !route.Enabled {
		return false, err
	}
	metadata, err := NormalizeModelCapabilities(route.Metadata)
	if err != nil {
		return false, nil
	}
	for _, candidate := range metadata.Operations {
		if candidate == operation {
			return true, nil
		}
	}
	return false, nil
}

func findPublicModelRoute(ctx context.Context, db *gorm.DB, requested string, clients []string) (*model.ClientModelRoute, error) {
	if db == nil || !db.Migrator().HasTable(&model.ClientModelRoute{}) || !db.Migrator().HasTable(&model.ClientModelTarget{}) {
		return nil, nil
	}
	var routes []model.ClientModelRoute
	query := db.WithContext(ctx).Select("id", "public_id", "aliases", "metadata", "client", "enabled")
	if len(clients) == 0 {
		query = query.Where("enabled = ?", true)
	}
	if err := query.Find(&routes).Error; err != nil {
		return nil, err
	}
	requested = strings.ToLower(strings.TrimSpace(requested))
	for _, route := range routes {
		if len(clients) != 0 && !RouteMatchesClient(route.Client, clients[0]) {
			continue
		}
		for _, name := range RouteNames(route) {
			if name == requested {
				return &route, nil
			}
		}
	}
	return nil, nil
}
