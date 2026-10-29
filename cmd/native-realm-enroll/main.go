// native-realm-enroll is an offline operator migration, never a web endpoint.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var dryRunRollback = errors.New("validated dry run")

func approvedCohort(raw []byte) bool {
	var m model.NativeRealmManifest
	if common.Unmarshal(raw, &m) != nil || m.Version != 1 || len(m.Users) != 13 {
		return false
	}
	users := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 8: true, 9: true, 10: true, 11: true, 12: true, 13: true, 14: true}
	keyOwners := map[int]int{1: 2, 2: 3, 3: 6, 4: 4, 5: 3, 6: 5, 7: 4, 8: 10, 9: 4, 10: 6, 11: 4, 12: 10, 13: 11, 14: 12, 15: 11, 16: 11, 17: 11, 18: 1}
	seen := map[int]bool{}
	for _, u := range m.Users {
		if !users[u.UserID] {
			return false
		}
		delete(users, u.UserID)
		for _, k := range u.Keys {
			if seen[k.ID] || keyOwners[k.ID] != u.UserID || k.Deleted != (k.ID == 3 || k.ID == 13) {
				return false
			}
			seen[k.ID] = true
		}
	}
	return len(users) == 0 && len(seen) == 18
}
func run() error {
	manifest := flag.String("manifest", "", "private reviewed manifest file")
	digest := flag.String("approved-sha256", "", "separately reviewed file SHA256")
	apply := flag.Bool("apply", false, "commit after all validations; default rolls back")
	flag.Parse()
	if *manifest == "" || len(*digest) != 64 {
		return errors.New("manifest and approved-sha256 are required")
	}
	info, err := os.Stat(*manifest)
	if err != nil || info.Size() > 4<<20 || info.Mode().Perm()&0077 != 0 {
		return errors.New("manifest must be a private file (0600), at most 4 MiB")
	}
	raw, err := os.ReadFile(*manifest)
	if err != nil || !approvedCohort(raw) {
		return errors.New("manifest does not match the reviewed native cohort")
	}
	dsn := os.Getenv("NATIVE_REALM_ENROLL_DSN")
	if dsn == "" {
		return errors.New("NATIVE_REALM_ENROLL_DSN is required")
	}
	var dialect gorm.Dialector
	switch os.Getenv("NATIVE_REALM_ENROLL_DATABASE") {
	case "postgres":
		dialect = postgres.Open(dsn)
	case "mysql":
		dialect = mysql.Open(dsn)
	case "sqlite":
		dialect = sqlite.Open(dsn)
	default:
		return errors.New("NATIVE_REALM_ENROLL_DATABASE must be postgres, mysql or sqlite")
	}
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		return errors.New("database connection failed")
	}
	sql, err := db.DB()
	if err != nil {
		return errors.New("database unavailable")
	}
	defer sql.Close()
	// Schema must be installed by the candidate application migration first.
	for _, field := range []string{"FundingRealm", "FundingRealmEvidence", "NativePassword"} {
		if !db.Migrator().HasColumn(&model.User{}, field) {
			return errors.New("candidate schema is not installed")
		}
	}
	if !db.Migrator().HasTable(&model.NativeLoginCredential{}) {
		return errors.New("candidate authenticator schema is not installed")
	}
	if !db.Migrator().HasColumn(&model.Token{}, "AuthRealm") || !db.Migrator().HasColumn(&model.UserSession{}, "AuthRealm") {
		return errors.New("candidate credential schema is not installed")
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if e := model.EnrollNativeRealm(context.Background(), tx, raw, *digest); e != nil {
			return e
		}
		if !*apply {
			return dryRunRollback
		}
		return nil
	})
	if errors.Is(err, dryRunRollback) {
		fmt.Println("VALIDATED: 13 native users, 18 reviewed keys; rolled back (no changes)")
		return nil
	}
	if err != nil {
		return errors.New("enrollment rejected or commit not confirmed; inspect privately and rerun validation, no values logged")
	}
	fmt.Println("COMMITTED: reviewed native realm enrolled; deleted keys remain deleted")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
