package router

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"gorm.io/gorm"
	"reflect"
	"slices"
	"strings"
)

// InstallKnowledgeBindings wires the actual DB resolver; no request can replace it.
func InstallKnowledgeBindings(a *platformbilling.Adapter) {
	a.SetKnowledgeBindingResolver(func(tx *gorm.DB, product, name, wire string) (platformbilling.ManagedBinding, string, error) {
		return resolveKnowledgeFacts(a, tx, product, name, wire)
	}, func(tx *gorm.DB, name string) error {
		if err := model.LockKnowledgeParents(tx, 0); err != nil {
			return err
		}
		var route model.ClientModelRoute
		if err := model.KnowledgeLockedRead(tx).First(&route, "public_id = ?", name).Error; err != nil {
			return err
		}
		return model.RotateKnowledgeRoute(tx, route.Id)
	})
}

// resolveKnowledgeFacts reads only the supplied transaction. It intentionally
// accepts the two bounded, single openai_chat targets used by this integration.
func resolveKnowledgeFacts(a *platformbilling.Adapter, tx *gorm.DB, product, name, wire string) (platformbilling.ManagedBinding, string, error) {
	principal, group := a.Principal()
	b := platformbilling.ManagedBinding{InstanceID: a.KnowledgeInstanceID(), OriginProductID: product, Model: name, WireOperation: wire}
	if !platformbilling.ApprovedKnowledgePair(name, wire) || (product != "llmrouter" && product != "superwagie") {
		return b, "", platformbilling.ErrInvalid
	}
	p, policyErr := a.ManagedKnowledgePolicy(name, wire)
	if policyErr != nil || p.ChannelType != 17 || (wire == "embeddings.create" && (p.ChannelID != 102 || p.Space == nil || p.Space.Dimensions != 1024)) || (wire == "rerank.create" && (p.ChannelID != 103)) {
		return b, "", platformbilling.ErrPending
	}
	if err := model.LockKnowledgeParents(tx, 0); err != nil {
		return b, "", err
	}
	var routes []model.ClientModelRoute
	if err := model.KnowledgeLockedRead(tx).Where("public_id = ?", name).Find(&routes).Error; err != nil {
		return b, "", err
	}
	if len(routes) != 1 {
		return b, "", platformbilling.ErrConflict
	}
	r := routes[0]
	if err := model.LockKnowledgeParents(tx, r.Id); err != nil {
		return b, "", err
	}
	if err := model.KnowledgeLockedRead(tx).First(&r, r.Id).Error; err != nil {
		return b, "", err
	}
	if !r.Enabled || (r.Client != "all" && r.Client != product) || !platformbilling.ValidKnowledgeID(r.KnowledgeGeneration) {
		return b, "", platformbilling.ErrConflict
	}
	var metadata dto.ModelCapabilities
	if common.UnmarshalUnique([]byte(r.Metadata), &metadata) != nil || !slices.Contains(metadata.Operations, wire) || !reflect.DeepEqual(metadata.EmbeddingSpace, p.Space) {
		return b, "", platformbilling.ErrConflict
	}
	// Aliases cannot introduce another candidate under either canonical model.
	var allRoutes []model.ClientModelRoute
	if err := model.KnowledgeLockedRead(tx).Where("enabled = ?", true).Order("id").Limit(1025).Find(&allRoutes).Error; err != nil {
		return b, "", err
	}
	if len(allRoutes) > 1024 {
		return b, "", platformbilling.ErrPending
	}
	for _, other := range allRoutes {
		if other.Id == r.Id {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(other.PublicId), name) {
			return b, "", platformbilling.ErrConflict
		}
		var aliases []string
		if other.Aliases != "" && common.UnmarshalUnique([]byte(other.Aliases), &aliases) != nil {
			return b, "", platformbilling.ErrConflict
		}
		for _, alias := range aliases {
			if strings.EqualFold(strings.TrimSpace(alias), name) {
				return b, "", platformbilling.ErrConflict
			}
		}
	}
	var targets []model.ClientModelTarget
	if err := model.KnowledgeLockedRead(tx).Where("route_id = ? AND enabled = ?", r.Id, true).Limit(2).Find(&targets).Error; err != nil {
		return b, "", err
	}
	if len(targets) != 1 {
		return b, "", platformbilling.ErrConflict
	}
	t := targets[0]
	var wires []string
	if t.ChannelId != p.ChannelID || t.UpstreamModel != p.UpstreamModel || t.BillingModel != p.UpstreamModel || common.UnmarshalUnique([]byte(t.PreferredWires), &wires) != nil || !slices.Equal(wires, []string{"openai_chat"}) || t.Metadata != "" || t.ContextFitRequired {
		return b, "", platformbilling.ErrConflict
	}
	var ch model.Channel
	if err := model.KnowledgeLockedRead(tx).First(&ch, p.ChannelID).Error; err != nil {
		return b, "", err
	}
	origin := ch.GetBaseURL()
	if origin == "" {
		origin = "https://dashscope.aliyuncs.com"
	}
	if ch.Type != 17 || ch.Status != common.ChannelStatusEnabled || ch.ChannelInfo.IsMultiKey || !a.KnowledgeTarget(name, wire, ch.Id, ch.Type, t.UpstreamModel, ch.Key, origin) || model_setting.GetGlobalSettings().PassThroughRequestEnabled {
		return b, "", platformbilling.ErrConflict
	}
	settings := ch.GetSetting()
	if settings.SystemPrompt != "" || settings.PassThroughBodyEnabled || settings.Proxy != "" {
		return b, "", platformbilling.ErrConflict
	}
	for _, override := range []*string{ch.ParamOverride, ch.HeaderOverride} {
		if override != nil && *override != "" {
			var fields map[string]any
			if common.UnmarshalUnique([]byte(*override), &fields) != nil || len(fields) != 0 {
				return b, "", platformbilling.ErrConflict
			}
		}
	}
	if ch.ModelMapping != nil && *ch.ModelMapping != "" && *ch.ModelMapping != "{}" {
		return b, "", platformbilling.ErrConflict
	}
	if !slices.Contains(strings.Split(ch.Models, ","), p.UpstreamModel) || !slices.Contains(strings.Split(ch.Group, ","), group) {
		return b, "", platformbilling.ErrConflict
	}
	var abilities []model.Ability
	if err := model.KnowledgeLockedRead(tx).Where("channel_id = ? AND model = ? AND enabled = ?", p.ChannelID, p.UpstreamModel, true).Find(&abilities).Error; err != nil {
		return b, "", err
	}
	var selected []model.Ability
	for _, ability := range abilities {
		if ability.Group == group {
			selected = append(selected, ability)
		}
	}
	if len(selected) != 1 {
		return b, "", platformbilling.ErrConflict
	}
	// No traffic counters, timestamps or balances enter this private digest.
	raw, err := common.Marshal(struct {
		Policy    platformbilling.KnowledgePolicy
		Route     any
		Target    model.ClientModelTarget
		Channel   any
		Ability   model.Ability
		Group     string
		Principal int
	}{p, []any{r.Id, r.PublicId, r.Client, r.Aliases, r.Metadata, r.Enabled, r.KnowledgeGeneration}, t, []any{ch.Id, ch.Type, ch.Key, ch.Status, origin, ch.Models, ch.Group, ch.ModelMapping, ch.Setting, ch.OtherSettings, ch.ParamOverride, ch.HeaderOverride, ch.ChannelInfo}, selected[0], group, principal})
	if err != nil {
		return b, "", platformbilling.ErrInvalid
	}
	sum := sha256.Sum256(raw)
	b.RouteID = r.Id
	b.TargetID = t.Id
	b.ChannelID = ch.Id
	b.RouteGeneration = r.KnowledgeGeneration
	if p.Space != nil {
		space := *p.Space
		b.EmbeddingSpace = &space
	}
	return b, hex.EncodeToString(sum[:]), nil
}
