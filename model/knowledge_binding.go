package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/knowledgebinding"
	"reflect"
	"slices"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// One shared pointer per product scope. History is immutable and never GC'd.
// The bounded activation run permits at most 64 historical pins per binding.
type KnowledgeActiveBinding = knowledgebinding.KnowledgeActiveBinding
type KnowledgeRoutePin = knowledgebinding.KnowledgeRoutePin

func KnowledgeModel(name string) bool {
	return name == "unihub-text-embedding-v4" || name == "qwen3-rerank"
}
func NewKnowledgeGeneration() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Shared bounded lock order: channels 102,103 ascending, then route ascending,
// then target/ability/pointer. SQLite acquires its write fence before reading.
// Call/attempt result paths never acquire these locks.
func LockKnowledgeParents(tx *gorm.DB, routeID int) error {
	if tx == nil {
		return gorm.ErrInvalidDB
	}
	tx = tx.Session(&gorm.Session{NewDB: true})
	for _, id := range []int{102, 103} {
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Model(&Channel{}).Where("id = ?", id).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
				return err
			}
		}
		var rows []Channel
		if err := lockForUpdate(tx).Select("id").Where("id = ?", id).Find(&rows).Error; err != nil {
			return err
		}
	}
	if routeID > 0 {
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Model(&ClientModelRoute{}).Where("id = ?", routeID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
				return err
			}
		}
		var row ClientModelRoute
		if err := lockForUpdate(tx).First(&row, routeID).Error; err != nil {
			return err
		}
	}
	return nil
}
func RetireKnowledgeRoute(tx *gorm.DB, id int) error {
	tx = tx.Session(&gorm.Session{NewDB: true})
	if !tx.Migrator().HasTable(&KnowledgeActiveBinding{}) {
		return nil
	}
	return tx.Model(&KnowledgeActiveBinding{}).Where("route_id = ?", id).Update("active_pin", "").Error
}
func retireKnowledgeChannels(tx *gorm.DB, ids ...int) error {
	tx = tx.Session(&gorm.Session{NewDB: true})
	if !tx.Migrator().HasTable(&KnowledgeActiveBinding{}) {
		return nil
	}
	if err := LockKnowledgeParents(tx, 0); err != nil {
		return err
	}
	names := []string{}
	for _, id := range ids {
		if id == 102 {
			names = append(names, "unihub-text-embedding-v4")
		}
		if id == 103 {
			names = append(names, "qwen3-rerank")
		}
	}
	if len(names) == 0 {
		return nil
	}
	return tx.Model(&KnowledgeActiveBinding{}).Where("model IN ?", names).Update("active_pin", "").Error
}
func (r *ClientModelRoute) BeforeCreate(tx *gorm.DB) error {
	if !KnowledgeModel(r.PublicId) {
		var aliases []string
		if r.Aliases != "" && common.UnmarshalUnique([]byte(r.Aliases), &aliases) != nil {
			return nil
		}
		for _, name := range append(aliases, r.PublicId) {
			if strings.EqualFold(strings.TrimSpace(name), "unihub-text-embedding-v4") || strings.EqualFold(strings.TrimSpace(name), "qwen3-rerank") {
				return retireKnowledgeChannels(tx, 102, 103)
			}
		}
		return nil
	}
	if err := LockKnowledgeParents(tx, 0); err != nil {
		return err
	}
	var err error
	r.KnowledgeGeneration, err = NewKnowledgeGeneration()
	return err
}

// Explicit route mutation APIs call this before modifying route or children.
func RotateKnowledgeRoute(tx *gorm.DB, id int) error {
	tx = tx.Session(&gorm.Session{NewDB: true})
	if err := LockKnowledgeParents(tx, id); err != nil {
		return err
	}
	var r ClientModelRoute
	if err := tx.First(&r, id).Error; err != nil {
		return err
	}
	if !KnowledgeModel(r.PublicId) && r.KnowledgeGeneration == "" {
		return nil
	}
	generation, err := NewKnowledgeGeneration()
	if err != nil {
		return err
	}
	if err = RetireKnowledgeRoute(tx, id); err != nil {
		return err
	}
	return tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Model(&ClientModelRoute{}).Where("id = ?", id).UpdateColumn("knowledge_generation", generation).Error
}

// Generic GORM update/delete paths (bulk/status/tag included) retire pins in
// their existing transaction. Traffic-only columns deliberately do not retire.
func (c *Channel) BeforeUpdate(tx *gorm.DB) error {
	auth := []string{"id", "tag", "type", "key", "status", "base_url", "models", "group", "model_mapping", "setting", "settings", "param_override", "header_override", "channel_info", "priority", "weight"}
	if len(tx.Statement.Selects) > 0 {
		relevant := false
		for _, column := range tx.Statement.Selects {
			if column == "*" || slices.Contains(auth, column) {
				relevant = true
			}
		}
		if !relevant {
			return nil
		}
	}
	if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
		relevant := false
		for key := range values {
			if field := tx.Statement.Schema.LookUpField(key); field != nil {
				key = field.DBName
			}
			if slices.Contains(auth, key) {
				relevant = true
			}
		}
		if !relevant {
			return nil
		}
	}
	return c.retireManaged(tx)
}
func (c *Channel) BeforeDelete(tx *gorm.DB) error { return c.retireManaged(tx) }
func (c *Channel) retireManaged(tx *gorm.DB) error {
	if c.Id != 0 && c.Id != 102 && c.Id != 103 {
		return nil
	}
	if c.Id == 0 {
		if where, ok := tx.Statement.Clauses["WHERE"].Expression.(clause.Where); ok && len(where.Exprs) == 1 {
			if expr, ok := where.Exprs[0].(clause.Expr); ok && expr.SQL == "id = ?" && len(expr.Vars) == 1 {
				if id, ok := expr.Vars[0].(int); ok && id != 102 && id != 103 {
					return nil
				}
			}
		}
	}

	// Restrict to matching managed IDs, using exactly the original predicate.
	q := tx.Session(&gorm.Session{NewDB: true}).Model(&Channel{})
	if c.Id != 0 {
		q = q.Where("id = ?", c.Id)
	} else if where, ok := tx.Statement.Clauses["WHERE"]; ok {
		q = q.Clauses(where.Expression)
	}
	var ids []int
	if err := q.Where("id IN ?", []int{102, 103}).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	tx.Statement.Settings.Store("knowledge_mutation_channel_ids", ids)
	return retireKnowledgeChannels(tx, ids...)
}
func (a *Ability) BeforeUpdate(tx *gorm.DB) error { return a.retireManaged(tx) }
func (a *Ability) BeforeDelete(tx *gorm.DB) error { return a.retireManaged(tx) }
func (a *Ability) BeforeCreate(tx *gorm.DB) error {
	if a.ChannelId != 102 && a.ChannelId != 103 {
		return nil
	}
	return retireKnowledgeChannels(tx, a.ChannelId)
}
func (a *Ability) retireManaged(tx *gorm.DB) error {
	if a.ChannelId != 0 && a.ChannelId != 102 && a.ChannelId != 103 {
		return nil
	}
	q := tx.Session(&gorm.Session{NewDB: true}).Model(&Ability{})
	if a.ChannelId != 0 {
		q = q.Where("channel_id = ?", a.ChannelId)
	} else if where, ok := tx.Statement.Clauses["WHERE"]; ok {
		q = q.Clauses(where.Expression)
	}
	var ids []int
	if err := q.Where("channel_id IN ?", []int{102, 103}).Distinct("channel_id").Pluck("channel_id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return retireKnowledgeChannels(tx, ids...)
}

func (t *ClientModelTarget) BeforeCreate(tx *gorm.DB) error {
	if t.RouteId == 0 {
		return nil
	}
	return rotateManagedRouteIfPresent(tx, t.RouteId)
}
func (t *ClientModelTarget) BeforeUpdate(tx *gorm.DB) error { return t.rotateParents(tx, true) }
func (t *ClientModelTarget) BeforeDelete(tx *gorm.DB) error { return t.rotateParents(tx, false) }
func (t *ClientModelTarget) rotateParents(tx *gorm.DB, updating bool) error {
	if err := LockKnowledgeParents(tx, 0); err != nil {
		return err
	}
	q := tx.Session(&gorm.Session{NewDB: true}).Model(&ClientModelTarget{})
	if t.Id != 0 {
		q = q.Where("id = ?", t.Id)
	} else if t.RouteId != 0 {
		q = q.Where("route_id = ?", t.RouteId)
	} else if where, ok := tx.Statement.Clauses["WHERE"]; ok {
		q = q.Clauses(where.Expression)
	}
	var targets []ClientModelTarget
	if err := q.Find(&targets).Error; err != nil {
		return err
	}
	ids := []int{}
	for _, target := range targets {
		if !slices.Contains(ids, target.RouteId) {
			ids = append(ids, target.RouteId)
		}
	}
	if updating {
		value, changed, err := knowledgeMutationValue(tx, "route_id")
		if err != nil {
			return err
		}
		if changed {
			destination, err := knowledgeParentID(value)
			if err != nil {
				return err
			}
			if !slices.Contains(ids, destination) {
				ids = append(ids, destination)
			}
		}
	}
	// Lock both old and incoming parents in one fixed order before retiring any.
	if err := lockKnowledgeAffectedParents(tx, ids); err != nil {
		return err
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := rotateManagedRouteIfPresent(tx, id); err != nil {
			return err
		}
	}
	return nil
}
func rotateManagedRouteIfPresent(tx *gorm.DB, id int) error {
	tx = tx.Session(&gorm.Session{NewDB: true})
	var r ClientModelRoute
	if err := tx.First(&r, id).Error; err != nil {
		return err
	}
	if !KnowledgeModel(r.PublicId) && r.KnowledgeGeneration == "" {
		return nil
	}
	return RotateKnowledgeRoute(tx, id)
}
func (r *ClientModelRoute) BeforeUpdate(tx *gorm.DB) error { return r.rotateMatched(tx, true) }
func (r *ClientModelRoute) BeforeDelete(tx *gorm.DB) error { return r.rotateMatched(tx, false) }
func (r *ClientModelRoute) rotateMatched(tx *gorm.DB, updating bool) error {
	if err := LockKnowledgeParents(tx, 0); err != nil {
		return err
	}
	q := tx.Session(&gorm.Session{NewDB: true}).Model(&ClientModelRoute{})
	if r.Id != 0 {
		q = q.Where("id = ?", r.Id)
	} else if where, ok := tx.Statement.Clauses["WHERE"]; ok {
		q = q.Clauses(where.Expression)
	}
	var rows []ClientModelRoute
	if err := q.Order("id").Find(&rows).Error; err != nil {
		return err
	}
	names := []string{}
	ids := []int{}
	for _, row := range rows {
		affected, err := knowledgeRouteNames(row.PublicId, row.Aliases)
		if err != nil {
			return err
		}
		names = append(names, affected...)
		if KnowledgeModel(row.PublicId) || row.KnowledgeGeneration != "" {
			ids = append(ids, row.Id)
		}
	}
	incomingManaged := false
	if updating {
		for _, field := range []string{"public_id", "aliases"} {
			value, changed, err := knowledgeMutationValue(tx, field)
			if err != nil {
				return err
			}
			if !changed {
				continue
			}
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("managed route %s mutation requires literal string", field)
			}
			public, aliases := "", ""
			if field == "public_id" {
				public = text
				incomingManaged = KnowledgeModel(text)
			} else {
				aliases = text
			}
			affected, err := knowledgeRouteNames(public, aliases)
			if err != nil {
				return err
			}
			names = append(names, affected...)
		}
	}
	if incomingManaged {
		for _, row := range rows {
			if !slices.Contains(ids, row.Id) {
				ids = append(ids, row.Id)
			}
		}
	}
	if len(names) > 0 {
		if err := LockKnowledgeParents(tx, 0); err != nil {
			return err
		}
		var canonical []ClientModelRoute
		if err := tx.Session(&gorm.Session{NewDB: true}).Where("public_id IN ?", names).Order("id").Find(&canonical).Error; err != nil {
			return err
		}
		for _, row := range canonical {
			if !slices.Contains(ids, row.Id) {
				ids = append(ids, row.Id)
			}
		}
	}
	if err := lockKnowledgeAffectedParents(tx, ids); err != nil {
		return err
	}
	slices.Sort(ids)
	for _, id := range ids {
		// Include a previously nonmanaged row becoming a managed identity.
		var current ClientModelRoute
		clean := tx.Session(&gorm.Session{NewDB: true})
		if err := lockForUpdate(clean).First(&current, id).Error; err != nil {
			return err
		}
		generation, err := NewKnowledgeGeneration()
		if err != nil {
			return err
		}
		if err = RetireKnowledgeRoute(clean, id); err != nil {
			return err
		}
		if err = clean.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Model(&ClientModelRoute{}).Where("id = ?", id).UpdateColumn("knowledge_generation", generation).Error; err != nil {
			return err
		}
		// Save(struct) otherwise writes its stale generation after the hook.
		if len(rows) == 1 && rows[0].Id == id && updating {
			tx.Statement.SetColumn("knowledge_generation", generation)
		}
	}

	return nil
}

// Channel authority and its derived abilities commit together, including bulk
// tag/status/save paths. Non-managed channels preserve their original paths.
func (c *Channel) AfterUpdate(tx *gorm.DB) error {
	value, ok := tx.Statement.Settings.Load("knowledge_mutation_channel_ids")
	if !ok {
		return nil
	}
	ids, ok := value.([]int)
	if !ok {
		return nil
	}
	clean := tx.Session(&gorm.Session{NewDB: true})
	for _, id := range ids {
		var channel Channel
		if err := clean.First(&channel, id).Error; err != nil {
			return err
		}
		if err := channel.UpdateAbilities(clean); err != nil {
			return err
		}
	}
	return nil
}
func (c *Channel) AfterDelete(tx *gorm.DB) error {
	value, ok := tx.Statement.Settings.Load("knowledge_mutation_channel_ids")
	if !ok {
		return nil
	}
	ids, ok := value.([]int)
	if !ok || len(ids) == 0 {
		return nil
	}
	return tx.Session(&gorm.Session{NewDB: true}).Where("channel_id IN ?", ids).Delete(&Ability{}).Error
}

// KnowledgeLockedRead prevents MySQL REPEATABLE READ snapshots from serving
// old authorization facts after the corresponding parent lock was acquired.
func KnowledgeLockedRead(tx *gorm.DB) *gorm.DB { return lockForUpdate(tx) }

// Read the same literal update selection as GORM: map keys may be Go names or
// DB names; struct Updates omit zero values unless selected; Save selects '*'.
// Expressions in parent/name columns are rejected rather than guessing scope.
func knowledgeMutationValue(tx *gorm.DB, column string) (any, bool, error) {
	selected, restricted := tx.Statement.SelectAndOmitColumns(false, true)
	if included, ok := selected[column]; ok && !included {
		return nil, false, nil
	} else if restricted && !included {
		return nil, false, nil
	}
	switch values := tx.Statement.Dest.(type) {
	case map[string]interface{}:
		for key, value := range values {
			field := tx.Statement.Schema.LookUpField(key)
			if field != nil && field.DBName == column {
				return value, true, nil
			}
		}
		return nil, false, nil
	}
	value := reflect.ValueOf(tx.Statement.Dest)
	for value.IsValid() && value.Kind() == reflect.Ptr {
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return nil, false, fmt.Errorf("unsupported managed route mutation")
	}
	destination := &gorm.Statement{DB: tx}
	if err := destination.Parse(tx.Statement.Dest); err != nil {
		return nil, false, err
	}
	field := destination.Schema.LookUpField(column)
	if field == nil {
		return nil, false, nil
	}
	raw, zero := field.ValueOf(tx.Statement.Context, value)
	if zero && !selected[column] {
		return nil, false, nil
	}
	return raw, true, nil
}
func knowledgeParentID(value any) (int, error) {
	reflected := reflect.ValueOf(value)
	if reflected.IsValid() {
		switch reflected.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n := reflected.Int()
			if n > 0 && int64(int(n)) == n {
				return int(n), nil
			}
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			n := reflected.Uint()
			if n > 0 && uint64(int(n)) == n && int(n) > 0 {
				return int(n), nil
			}
		}
	}
	return 0, fmt.Errorf("managed target route_id mutation requires positive literal integer")
}
func knowledgeRouteNames(public, aliases string) ([]string, error) {
	names := []string{public}
	var decoded []string
	if aliases != "" {
		if err := common.UnmarshalUnique([]byte(aliases), &decoded); err != nil {
			return nil, err
		}
		names = append(names, decoded...)
	}
	affected := []string{}
	for _, name := range names {
		normalized := strings.ToLower(strings.TrimSpace(name))
		if KnowledgeModel(normalized) && !slices.Contains(affected, normalized) {
			affected = append(affected, normalized)
		}
	}
	return affected, nil
}
func lockKnowledgeAffectedParents(tx *gorm.DB, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	if err := LockKnowledgeParents(tx, 0); err != nil {
		return err
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := LockKnowledgeParents(tx, id); err != nil {
			return err
		}
	}
	return nil
}
