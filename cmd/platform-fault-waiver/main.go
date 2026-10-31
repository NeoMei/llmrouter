// platform-fault-waiver is an offline OS-root operator entry. Financial
// authorization is separately checked by UniHub's persisted finance:write role.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Geteuid()); err != nil {
		log.Print("platform fault waiver rejected or pending; inspect approved case privately")
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, euid int) error {
	if euid != 0 {
		return errors.New("root operator required")
	}
	flags := flag.NewFlagSet("platform-fault-waiver", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("approval", "", "private approved case JSON")
	digest := flags.String("approval-sha256", "", "reviewed raw approval SHA-256")
	mode := flags.String("mode", "dry-run", "dry-run, install-schema, stage or finalize")
	apply := flags.Bool("apply", false, "explicitly permit the selected write")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *path == "" || *digest == "" || (*mode != "dry-run" && *mode != "install-schema" && *mode != "stage" && *mode != "finalize") || ((*mode != "dry-run") != *apply) {
		return errors.New("invalid arguments")
	}
	raw, err := readPrivateApproval(*path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	var approval struct {
		OperatorID string `json:"operator_id"`
		CaseID     string `json:"case_id"`
	}
	if hex.EncodeToString(sum[:]) != *digest || common.UnmarshalUnique(raw, &approval) != nil || approval.OperatorID == "" || approval.OperatorID != getenv("UNIHUB_WAIVER_OPERATOR_ID") {
		return errors.New("approved operator mismatch")
	}
	if err := platformbilling.ValidatePlatformFaultWaiverApproval(raw, *digest); err != nil {
		return errors.New("invalid approval")
	}
	dsn := getenv("PLATFORM_FAULT_WAIVER_SQL_DSN")
	conf := &gorm.Config{Logger: logger.Discard}
	var db *gorm.DB
	switch {
	case strings.HasPrefix(dsn, "postgres:") || strings.HasPrefix(dsn, "postgresql:"):
		db, err = gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), conf)
	case strings.HasPrefix(dsn, "mysql:") || strings.Contains(dsn, "@tcp(") || strings.Contains(dsn, "@unix("):
		db, err = gorm.Open(mysql.Open(dsn), conf)
	case strings.HasPrefix(dsn, "sqlite:") && len(dsn) > len("sqlite:"):
		db, err = gorm.Open(sqlite.Open(strings.TrimPrefix(dsn, "sqlite:")), conf)
	default:
		return errors.New("operator database unavailable")
	}
	if err != nil {
		return errors.New("operator database unavailable")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return errors.New("operator database unavailable")
	}
	defer sqlDB.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if *mode == "install-schema" {
		if err := platformbilling.InstallPlatformFaultWaiverSchema(ctx, db); err != nil {
			return errors.New("waiver schema unavailable")
		}
		fmt.Println(`{"status":"schema-installed"}`)
		return nil
	}
	var call platformbilling.Call
	if *mode == "finalize" {
		cfg := platformbilling.Config{Origin: getenv("PLATFORM_FAULT_WAIVER_UNIHUB_ORIGIN"), ServiceToken: getenv("PLATFORM_FAULT_WAIVER_SERVICE_TOKEN")}
		call, err = platformbilling.FinalizePlatformFaultWaiver(ctx, db, cfg, raw, *digest)
	} else {
		call, err = platformbilling.PreparePlatformFaultWaiver(ctx, db, raw, *digest, *apply)
	}
	if err != nil {
		return errors.New("approved case rejected or pending")
	}
	result, err := common.Marshal(map[string]string{"case_id": approval.CaseID, "call_id": call.CallID, "status": call.Status, "charged": call.Charged, "provider_state": "PROVIDER_UNRESOLVED", "approval_sha256": *digest})
	if err != nil {
		return errors.New("result unavailable")
	}
	fmt.Println(string(result))
	return nil
}

func readPrivateApproval(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("private approval unavailable")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("private approval unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 || opened.Size() < 1 || opened.Size() > 4<<20 {
		return nil, errors.New("private approval unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, errors.New("private approval unavailable")
	}
	return raw, nil
}
