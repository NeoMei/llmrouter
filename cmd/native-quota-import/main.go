// native-quota-import is an explicit, finite offline operator migration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var dryRunRollback = errors.New("validated quota import dry run")

// This CLI is bound to the reviewed original cohort. New-key fixture activation
// uses the model API; an empty manifest is never production cohort acceptance.
func approvedCohort(raw []byte, cutover string) bool {
	var manifest model.NativeTokenQuotaManifest
	if common.Unmarshal(raw, &manifest) != nil || manifest.Version != 1 || manifest.CutoverEvidence != cutover || len(manifest.Inventory) != 18 || len(manifest.Tokens) != 16 {
		return false
	}
	owners := map[int]int{1: 2, 2: 3, 3: 6, 4: 4, 5: 3, 6: 5, 7: 4, 8: 10, 9: 4, 10: 6, 11: 4, 12: 10, 13: 11, 14: 12, 15: 11, 16: 11, 17: 11, 18: 1}
	inventory := make(map[int]model.NativeTokenQuotaInventory, 18)
	for _, key := range manifest.Inventory {
		owner, known := owners[key.TokenID]
		if _, exists := inventory[key.TokenID]; exists || !known || owner != key.UserID || len(key.KeySHA256) != 64 || key.Deleted != (key.TokenID == 3 || key.TokenID == 13) {
			return false
		}
		inventory[key.TokenID] = key
	}
	seen := make(map[int]bool, 16)
	for _, key := range manifest.Tokens {
		original, exists := inventory[key.TokenID]
		if !exists || original.Deleted || seen[key.TokenID] || original.UserID != key.UserID || original.KeySHA256 != key.KeySHA256 {
			return false
		}
		seen[key.TokenID] = true
	}
	return len(seen) == 16 && len(inventory) == 18
}

func run() error {
	manifestPath := flag.String("manifest", "", "private approved frozen manifest, mode 0600")
	approved := flag.String("approved-sha256", "", "separately approved manifest SHA256")
	cutover := flag.String("approved-cutover-sha256", "", "separately approved old-writer drain/fence evidence SHA256")
	apply := flag.Bool("apply", false, "commit after validation; default rolls back")
	flag.Parse()
	if *manifestPath == "" || len(*approved) != 64 || len(*cutover) != 64 {
		return errors.New("manifest, approved-sha256 and approved-cutover-sha256 are required")
	}
	info, err := os.Lstat(*manifestPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > 4<<20 {
		return errors.New("manifest must be a regular private 0600 file, at most 4 MiB")
	}
	file, err := os.Open(*manifestPath)
	if err != nil {
		return errors.New("manifest unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("manifest changed during validation")
	}
	raw := make([]byte, info.Size())
	if _, err = file.ReadAt(raw, 0); err != nil {
		return errors.New("manifest read failed")
	}
	if !approvedCohort(raw, *cutover) {
		return errors.New("manifest does not match the reviewed 18-key inventory and 16 live-key quota cohort")
	}
	dsn := os.Getenv("NATIVE_QUOTA_IMPORT_DSN")
	if dsn == "" {
		return errors.New("NATIVE_QUOTA_IMPORT_DSN is required")
	}
	var dialect gorm.Dialector
	switch os.Getenv("NATIVE_QUOTA_IMPORT_DATABASE") {
	case "postgres":
		dialect = postgres.Open(dsn)
	case "mysql":
		dialect = mysql.Open(dsn)
	case "sqlite":
		dialect = sqlite.Open(dsn)
	default:
		return errors.New("NATIVE_QUOTA_IMPORT_DATABASE must be postgres, mysql or sqlite")
	}
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		return errors.New("database connection failed")
	}
	connection, err := db.DB()
	if err != nil {
		return errors.New("database unavailable")
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db = db.WithContext(ctx)
	for _, table := range []any{&model.NativeTokenQuotaProtocol{}, &model.NativeTokenQuotaState{}, &model.NativeTokenQuotaOperation{}, &model.NativeTokenQuotaSession{}, &model.NativeTokenFundingOperation{}} {
		if !db.Migrator().HasTable(table) {
			return errors.New("candidate quota schema must be installed before import")
		}
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := model.ImportNativeTokenQuota(ctx, tx, raw, *approved); err != nil {
			return err
		}
		if !*apply {
			return dryRunRollback
		}
		return nil
	})
	if errors.Is(err, dryRunRollback) {
		fmt.Println("VALIDATED: 18 original keys, 16 live quota states; rolled back (no changes)")
		return nil
	}
	if err != nil {
		return errors.New("quota import rejected or commit unconfirmed; inspect privately, no automatic replay or values logged")
	}
	fmt.Println("COMMITTED: 16 reviewed live native key quota states; original deleted keys preserved")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
