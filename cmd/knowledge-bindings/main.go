package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/knowledgeoperator"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		if errors.Is(err, knowledgeoperator.ErrUnknownCommit) {
			log.Print("knowledge activation commit outcome unknown; do not resend")
		} else {
			log.Print("knowledge activation failed; details withheld")
		}
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string) error {
	flags := flag.NewFlagSet("knowledge-bindings", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	approvalPath := flags.String("approval", "", "private approval JSON")
	approvalSHA := flags.String("approval-sha256", "", "raw approval SHA-256")
	apply := flags.Bool("apply", false, "commit approved activation")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *approvalPath == "" || *approvalSHA == "" {
		return errors.New("invalid arguments")
	}
	raw, err := readPrivateApproval(*approvalPath)
	if err != nil {
		return errors.New("approval unavailable")
	}
	revision, err := knowledgeoperator.SourceRevision()
	if err != nil {
		return errors.New("source revision unavailable")
	}
	cfg, err := platformbilling.ConfigFromEnv(getenv)
	if err != nil || !cfg.Enabled {
		return errors.New("operator configuration unavailable")
	}
	dsn := getenv("KNOWLEDGE_OPERATOR_SQL_DSN")
	if dsn == "" {
		return errors.New("operator database unavailable")
	}
	db, dbType, err := openDatabase(dsn)
	if err != nil {
		return errors.New("operator database unavailable")
	}
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}()
	common.SetMainDatabaseType(dbType)
	common.SetLogDatabaseType(dbType)
	model.InitDatabaseColumns()
	result, err := knowledgeoperator.Run(context.Background(), db, cfg, raw, *approvalSHA, revision, *apply)
	if err != nil {
		if errors.Is(err, knowledgeoperator.ErrUnknownCommit) {
			return err
		}
		return errors.New("approval or database state rejected")
	}
	encoded, err := common.Marshal(result)
	if err != nil {
		return errors.New("result unavailable")
	}
	fmt.Println(string(encoded))
	return nil
}

func openDatabase(dsn string) (*gorm.DB, common.DatabaseType, error) {
	conf := &gorm.Config{Logger: logger.Discard, DisableAutomaticPing: false}
	switch {
	case strings.HasPrefix(dsn, "postgres:") || strings.HasPrefix(dsn, "postgresql:"):
		db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), conf)
		return db, common.DatabaseTypePostgreSQL, err
	case strings.HasPrefix(dsn, "mysql:") || strings.Contains(dsn, "@tcp(") || strings.Contains(dsn, "@unix("):
		db, err := gorm.Open(mysql.Open(dsn), conf)
		return db, common.DatabaseTypeMySQL, err
	case strings.HasPrefix(dsn, "sqlite:") && strings.TrimSpace(strings.TrimPrefix(dsn, "sqlite:")) != "":
		db, err := gorm.Open(sqlite.Open(strings.TrimPrefix(dsn, "sqlite:")), conf)
		return db, common.DatabaseTypeSQLite, err
	default:
		return nil, "", errors.New("unsupported operator database")
	}
}

func readPrivateApproval(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("invalid approval file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("invalid approval file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 || opened.Size() < 1 || opened.Size() > 16<<10 {
		return nil, errors.New("invalid approval file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return nil, errors.New("invalid approval file")
	}
	return raw, nil
}
