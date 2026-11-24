package model

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

const (
	NameRuleExact = iota
	NameRulePrefix
	NameRuleContains
	NameRuleSuffix
)

type BoundChannel struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Type   int    `json:"type"`
	Status int    `json:"status"`
}

type Model struct {
	Id           int            `json:"id"`
	ModelName    string         `json:"model_name" gorm:"size:128;not null;uniqueIndex:uk_model_name_delete_at,priority:1"`
	Description  string         `json:"description,omitempty" gorm:"type:text"`
	Icon         string         `json:"icon,omitempty" gorm:"type:varchar(128)"`
	Tags         string         `json:"tags,omitempty" gorm:"type:varchar(255)"`
	VendorID     int            `json:"vendor_id,omitempty" gorm:"index"`
	Endpoints    string         `json:"endpoints,omitempty" gorm:"type:text"`
	Status       int            `json:"status" gorm:"default:1"`
	SyncOfficial int            `json:"sync_official" gorm:"default:1"`
	CreatedTime  int64          `json:"created_time" gorm:"bigint"`
	UpdatedTime  int64          `json:"updated_time" gorm:"bigint"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index;uniqueIndex:uk_model_name_delete_at,priority:2"`

	BoundChannels []BoundChannel `json:"bound_channels,omitempty" gorm:"-"`
	EnableGroups  []string       `json:"enable_groups,omitempty" gorm:"-"`
	QuotaTypes    []int          `json:"quota_types,omitempty" gorm:"-"`
	NameRule      int            `json:"name_rule" gorm:"default:0"`

	MatchedModels []string `json:"matched_models,omitempty" gorm:"-"`
	MatchedCount  int      `json:"matched_count,omitempty" gorm:"-"`
}

// MatchesName applies a metadata rule to a configured model name.
func (mi *Model) MatchesName(name string) bool {
	switch mi.NameRule {
	case NameRuleExact:
		return name == mi.ModelName
	case NameRulePrefix:
		return strings.HasPrefix(name, mi.ModelName)
	case NameRuleSuffix:
		return strings.HasSuffix(name, mi.ModelName)
	case NameRuleContains:
		return strings.Contains(name, mi.ModelName)
	default:
		return false
	}
}

// resolveModelMetadata keeps the existing exact, prefix, suffix, contains
// precedence. Callers supply rows in ID order to break ties consistently.
func resolveModelMetadata(rows []Model, names []string) map[string]*Model {
	result := make(map[string]*Model, len(names))
	rules := make(map[int][]*Model)
	for i := range rows {
		item := &rows[i]
		if item.NameRule == NameRuleExact {
			if result[item.ModelName] == nil {
				result[item.ModelName] = item
			}
		} else {
			rules[item.NameRule] = append(rules[item.NameRule], item)
		}
	}
	for _, rule := range []int{NameRulePrefix, NameRuleSuffix, NameRuleContains} {
		for _, item := range rules[rule] {
			for _, name := range names {
				if result[name] == nil && item.MatchesName(name) {
					result[name] = item
				}
			}
		}
	}
	return result
}

// EndpointOverrides accepts both the legacy path string and the object form.
func (mi *Model) EndpointOverrides() map[string]common.EndpointInfo {
	var raw map[string]any
	if mi.Endpoints == "" || common.UnmarshalJsonStr(mi.Endpoints, &raw) != nil {
		return nil
	}
	endpoints := make(map[string]common.EndpointInfo)
	for name, value := range raw {
		info := common.EndpointInfo{Method: "POST"}
		switch v := value.(type) {
		case string:
			info.Path = strings.TrimSpace(v)
		case map[string]any:
			path, _ := v["path"].(string)
			info.Path = strings.TrimSpace(path)
			method, _ := v["method"].(string)
			if method = strings.TrimSpace(method); method != "" {
				info.Method = strings.ToUpper(method)
			}
		}
		if name != "" && info.Path != "" {
			endpoints[name] = info
		}
	}
	return endpoints
}

func (mi *Model) Insert() error {
	return DB.Transaction(mi.InsertWithDB)
}

// InsertWithDB creates metadata inside the caller's configuration transaction.
func (mi *Model) InsertWithDB(tx *gorm.DB) error {
	if err := lockModelVendor(tx, mi.VendorID); err != nil {
		return err
	}
	now := common.GetTimestamp()
	mi.CreatedTime = now
	mi.UpdatedTime = now

	// GORM fills zero values from schema defaults during Create.
	status, syncOfficial := mi.Status, mi.SyncOfficial
	if err := tx.Create(mi).Error; err != nil {
		return err
	}
	if status == mi.Status && syncOfficial == mi.SyncOfficial {
		return nil
	}
	mi.Status, mi.SyncOfficial = status, syncOfficial
	return tx.Model(&Model{}).Where("id = ?", mi.Id).Updates(map[string]any{
		"status": status, "sync_official": syncOfficial,
	}).Error
}

func IsModelNameDuplicated(id int, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	var cnt int64
	err := DB.Model(&Model{}).Where("model_name = ? AND id <> ?", name, id).Count(&cnt).Error
	return cnt > 0, err
}

// GetModelMetadataForUpdate loads the current metadata in a write transaction.
func GetModelMetadataForUpdate(tx *gorm.DB, name string) (*Model, error) {
	var metadata Model
	if err := lockForUpdate(tx).Where("model_name = ?", name).First(&metadata).Error; err != nil {
		return nil, err
	}
	return &metadata, nil
}

func (mi *Model) Update() error {
	return DB.Transaction(mi.UpdateWithDB)
}

func (mi *Model) UpdateWithDB(tx *gorm.DB) error {
	var existing Model
	if err := lockForUpdate(tx).First(&existing, mi.Id).Error; err != nil {
		return err
	}
	if mi.VendorID != existing.VendorID {
		if err := lockModelVendor(tx, mi.VendorID); err != nil {
			return err
		}
	}
	if existing.NameRule == NameRuleExact && (mi.ModelName != existing.ModelName || mi.NameRule != existing.NameRule) {
		if err := existing.checkActiveBindings(tx); err != nil {
			return err
		}
	}
	mi.UpdatedTime = common.GetTimestamp()
	return tx.Model(&Model{}).Where("id = ?", mi.Id).
		Select("model_name", "description", "icon", "tags", "vendor_id", "endpoints", "status", "sync_official", "name_rule", "updated_time").
		Updates(mi).Error
}

func (mi *Model) Delete() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var existing Model
		if err := lockForUpdate(tx).First(&existing, mi.Id).Error; err != nil {
			return err
		}
		if err := existing.checkActiveBindings(tx); err != nil {
			return err
		}
		return tx.Delete(&existing).Error
	})
}

func (mi *Model) checkActiveBindings(tx *gorm.DB) error {
	if mi.NameRule != NameRuleExact {
		return nil
	}
	var ids []int
	err := tx.Table("abilities").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where("abilities.model = ? AND abilities.enabled = ? AND channels.status = ?", mi.ModelName, true, common.ChannelStatusEnabled).
		Limit(1).Pluck("channels.id", &ids).Error
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		return fmt.Errorf("model %q is bound to an enabled channel", mi.ModelName)
	}

	// Explicit routes may refer to a separate public, upstream or billing ID.
	err = tx.Table("client_model_targets").
		Joins("JOIN client_model_routes ON client_model_routes.id = client_model_targets.route_id").
		Joins("JOIN channels ON channels.id = client_model_targets.channel_id").
		Where("client_model_routes.enabled = ? AND client_model_targets.enabled = ? AND channels.status = ?", true, true, common.ChannelStatusEnabled).
		Where("(client_model_routes.public_id = ? OR client_model_targets.upstream_model = ? OR client_model_targets.billing_model = ?)", mi.ModelName, mi.ModelName, mi.ModelName).
		Limit(1).Pluck("client_model_routes.id", &ids).Error
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		return fmt.Errorf("model %q is referenced by an enabled client route", mi.ModelName)
	}
	return nil
}

func GetVendorModelCounts() (map[int64]int64, error) {
	var stats []struct {
		VendorID int64
		Count    int64
	}
	if err := DB.Model(&Model{}).
		Select("vendor_id as vendor_id, count(*) as count").
		Group("vendor_id").
		Scan(&stats).Error; err != nil {
		return nil, err
	}
	m := make(map[int64]int64, len(stats))
	for _, s := range stats {
		m[s.VendorID] = s.Count
	}
	return m, nil
}

func GetAllModels(offset int, limit int) ([]*Model, error) {
	models, _, err := SearchModels("", "", "", "", offset, limit)
	return models, err
}

func GetBoundChannelsByModelsMap(modelNames []string) (map[string][]BoundChannel, error) {
	result := make(map[string][]BoundChannel)
	if len(modelNames) == 0 {
		return result, nil
	}
	type row struct {
		Model string
		BoundChannel
	}
	var rows []row
	err := DB.Table("channels").
		Select("abilities.model as model, channels.id as id, channels.name as name, channels.type as type, channels.status as status").
		Joins("JOIN abilities ON abilities.channel_id = channels.id").
		Where("abilities.model IN ? AND abilities.enabled = ? AND channels.status = ?", modelNames, true, common.ChannelStatusEnabled).
		Distinct().
		Order("channels.id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		result[r.Model] = append(result[r.Model], r.BoundChannel)
	}
	return result, nil
}

func normalizeLookupValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func GetPreferredModelOwnerChannelTypes(modelNames []string, groups []string) (map[string]int, error) {
	result := make(map[string]int)
	modelNames = normalizeLookupValues(modelNames)
	if len(modelNames) == 0 {
		return result, nil
	}

	type row struct {
		Model       string
		ChannelType int
	}
	var rows []row

	query := DB.Table("abilities").
		Select("abilities.model as model, channels.type as channel_type").
		Joins("JOIN channels ON abilities.channel_id = channels.id").
		Where("abilities.model IN ? AND abilities.enabled = ? AND channels.status = ?", modelNames, true, common.ChannelStatusEnabled).
		Order("COALESCE(abilities.priority, 0) DESC").
		Order("abilities.weight DESC").
		Order("abilities.channel_id ASC")

	groups = normalizeLookupValues(groups)
	if len(groups) > 0 {
		query = query.Where("abilities."+commonGroupCol+" IN ?", groups)
	}

	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}

	for _, r := range rows {
		if _, ok := result[r.Model]; ok {
			continue
		}
		result[r.Model] = r.ChannelType
	}
	return result, nil
}

func SearchModels(keyword string, vendor string, status string, syncOfficial string, offset int, limit int) ([]*Model, int64, error) {
	var models []*Model
	db := DB.Model(&Model{})
	if keyword != "" {
		like := "%" + keyword + "%"
		db = db.Where("model_name LIKE ? OR description LIKE ? OR tags LIKE ?", like, like, like)
	}
	if vendor != "" {
		if vid, err := strconv.Atoi(vendor); err == nil {
			db = db.Where("models.vendor_id = ?", vid)
		} else {
			db = db.Joins("JOIN vendors ON vendors.id = models.vendor_id AND vendors.deleted_at IS NULL").Where("vendors.name LIKE ?", "%"+vendor+"%")
		}
	}
	if statusValue, ok := parseModelStatusFilter(status); ok {
		db = db.Where("models.status = ?", statusValue)
	}
	if syncValue, ok := parseModelSyncFilter(syncOfficial); ok {
		db = db.Where("models.sync_official = ?", syncValue)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := db.Order("models.id DESC").Offset(offset).Limit(limit).Find(&models).Error; err != nil {
		return nil, 0, err
	}
	return models, total, nil
}

// parseModelStatusFilter maps UI/API status values to the models.status column.
// Returns ok=false when no status filter should be applied.
func parseModelStatusFilter(status string) (value int, ok bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "all":
		return 0, false
	case "enabled", "1":
		return 1, true
	case "disabled", "0":
		return 0, true
	default:
		n, err := strconv.Atoi(status)
		if err != nil {
			return 0, false
		}
		return n, true
	}
}

// parseModelSyncFilter maps UI/API sync values to the models.sync_official column.
// Returns ok=false when no sync filter should be applied.
func parseModelSyncFilter(syncOfficial string) (value int, ok bool) {
	switch strings.ToLower(strings.TrimSpace(syncOfficial)) {
	case "", "all":
		return 0, false
	case "yes", "1":
		return 1, true
	case "no", "0":
		return 0, true
	default:
		n, err := strconv.Atoi(syncOfficial)
		if err != nil {
			return 0, false
		}
		return n, true
	}
}
