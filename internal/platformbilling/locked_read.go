package platformbilling

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A fresh query is required for every table: retaining a GORM query after Find
// also retains its table. model.lockForUpdate cannot be imported here because
// model already depends on platformbilling through relay/common.
func platformBillingLockedRead(tx *gorm.DB) *gorm.DB {
	q := tx.Session(&gorm.Session{NewDB: true})
	if tx.Dialector.Name() != "sqlite" {
		return q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return q
}
