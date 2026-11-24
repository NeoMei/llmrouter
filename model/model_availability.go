package model

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

// DisabledModelNames returns explicit model stops. Pattern rules remain
// presentation rules and cannot disable unrelated model IDs.
func DisabledModelNames(ctx context.Context, db *gorm.DB) (map[string]bool, error) {
	if db == nil || !db.Migrator().HasTable(&Model{}) {
		return nil, nil
	}
	var names []string
	err := db.WithContext(ctx).Model(&Model{}).
		Where("name_rule = ? AND status <> ?", NameRuleExact, 1).
		Pluck("model_name", &names).Error
	if err != nil {
		return nil, err
	}
	disabled := make(map[string]bool, len(names))
	for _, name := range names {
		disabled[strings.ToLower(strings.TrimSpace(name))] = true
	}
	return disabled, nil
}
