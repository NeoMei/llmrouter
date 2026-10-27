// product-runtime-migrate applies an explicitly reviewed account mapping. It
// never discovers identity matches by email and defaults to a read-only plan.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

type migrationManifest struct {
	BatchID         string                  `json:"batch_id"`
	Drained         bool                    `json:"drained"`
	OfflineEvidence *offlineCutoverEvidence `json:"offline_evidence,omitempty"`
	Users           []migrationUser         `json:"users"`
}

// Financial migration is deliberately an offline operation. A new CLI process
// cannot observe another gateway's in-memory batch queue. This reviewed receipt
// records the operator's drain/reconciliation evidence; it is not a live-drain
// detector, and must never be synthesized from this process's empty queue.
type offlineCutoverEvidence struct {
	AllWritersStopped bool   `json:"all_writers_stopped"`
	BatchReconciled   bool   `json:"batch_reconciled"`
	CachesReconciled  bool   `json:"caches_reconciled"`
	BackupSHA256      string `json:"backup_sha256"`
	SnapshotSHA256    string `json:"snapshot_sha256"`
	EvidenceRef       string `json:"evidence_ref"`
}
type migrationUser struct {
	LocalUserID         int                        `json:"local_user_id"`
	CentralUserID       string                     `json:"central_user_id"`
	ExpectedUsername    string                     `json:"expected_username"`
	ExpectedQuota       int                        `json:"expected_quota"`
	ExpectedRole        int                        `json:"expected_role"`
	ExpectedAuthVersion int64                      `json:"expected_auth_version"`
	PermissionOverrides map[string]map[string]bool `json:"permission_overrides"`
	ImportBalance       bool                       `json:"import_balance"`
}

func validateManifest(in migrationManifest) error {
	if in.BatchID == "" || len(in.BatchID) > 128 || len(in.Users) == 0 || len(in.Users) > 10000 {
		return errors.New("invalid reviewed migration manifest")
	}
	seen := map[int]bool{}
	for _, u := range in.Users {
		if u.LocalUserID <= 0 || seen[u.LocalUserID] || u.ExpectedUsername == "" || u.ExpectedAuthVersion < 1 || u.ExpectedQuota < 0 || (u.ExpectedRole != common.RoleCommonUser && u.ExpectedRole != common.RoleAdminUser && u.ExpectedRole != common.RoleRootUser) {
			return errors.New("invalid or duplicate reviewed user snapshot")
		}
		if u.ImportBalance {
			e := in.OfflineEvidence
			if !in.Drained || e == nil || !e.AllWritersStopped || !e.BatchReconciled || !e.CachesReconciled || e.EvidenceRef == "" {
				return errors.New("financial cutover requires reviewed offline writer, batch and cache reconciliation evidence")
			}
			if raw, err := hex.DecodeString(e.BackupSHA256); err != nil || len(raw) != sha256.Size || e.SnapshotSHA256 != manifestSnapshotDigest(in) {
				return errors.New("financial cutover evidence does not match the reviewed backup and user snapshot")
			}
		}
		seen[u.LocalUserID] = true
	}
	return nil
}

func manifestSnapshotDigest(in migrationManifest) string {
	users := append([]migrationUser(nil), in.Users...)
	sort.Slice(users, func(i, j int) bool { return users[i].LocalUserID < users[j].LocalUserID })
	raw, _ := common.Marshal(struct {
		BatchID string          `json:"batch_id"`
		Users   []migrationUser `json:"users"`
	}{in.BatchID, users})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func run(ctx context.Context, in migrationManifest, apply bool) error {
	if err := validateManifest(in); err != nil {
		return err
	}
	var client *userkit.RuntimeClient
	if apply {
		var err error
		client, err = platformaccount.RuntimeClient()
		if err != nil {
			return err
		}
	}
	var reviewedUsers []*model.User
	for _, entry := range in.Users {
		u, err := model.GetUserById(entry.LocalUserID, true)
		if err != nil {
			return err
		}
		if u.Username != entry.ExpectedUsername || u.Role != entry.ExpectedRole || u.AuthVersion != entry.ExpectedAuthVersion {
			return fmt.Errorf("user %d changed after review", entry.LocalUserID)
		}
		var existing model.ProductFundingAccount
		fundingErr := model.DB.First(&existing, "user_id = ?", u.Id).Error
		if fundingErr != nil && !errors.Is(fundingErr, gorm.ErrRecordNotFound) {
			return fundingErr
		}
		alreadyImported := fundingErr == nil && existing.State == "central" && existing.BatchID == in.BatchID && existing.SnapshotQuota == entry.ExpectedQuota
		if u.Quota != entry.ExpectedQuota && !alreadyImported {
			return fmt.Errorf("user %d balance changed after review", entry.LocalUserID)
		}
		reviewedUsers = append(reviewedUsers, u)
	}
	// Validate the entire reviewed snapshot before the first remote mutation.
	for i, entry := range in.Users {
		u := reviewedUsers[i]
		if !apply {
			fmt.Printf("user %d: validated; identity mapping=%t; financial cutover=%t\n", u.Id, entry.CentralUserID != "", entry.ImportBalance)
			continue
		}
		raw, err := common.Marshal(struct {
			Entry    migrationUser
			Evidence *offlineCutoverEvidence
			Snapshot string
		}{entry, in.OfflineEvidence, manifestSnapshotDigest(in)})
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		identity, err := client.ImportProductIdentity(ctx, userkit.ProductIdentityImport{ProductIdentityRegistration: userkit.ProductIdentityRegistration{LocalUserID: strconv.Itoa(u.Id), OperationID: "reviewed-import:" + in.BatchID + ":" + strconv.Itoa(u.Id), Username: u.Username, DisplayName: u.DisplayName, Email: u.Email, AuthVersion: u.AuthVersion}, CentralUserID: entry.CentralUserID, ReviewedBatchID: in.BatchID, SnapshotDigest: digest, ProductRole: entry.ExpectedRole, Active: u.Status == common.UserStatusEnabled, PermissionOverrides: entry.PermissionOverrides})
		if err != nil {
			return err
		}
		if _, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: identity.Access.Issuer, User: platformaccount.User{ID: identity.Access.Subject, Name: identity.Access.User.Name, Email: identity.Access.User.Email}}, u.Id); err != nil {
			return err
		}
		if entry.ImportBalance {
			if err = model.NewProductFundingStore(model.DB, client).Import(ctx, u.Id, identity.Binding.PersonalAccountID, in.BatchID, string(raw)); err != nil {
				return err
			}
		}
		fmt.Printf("user %d: reviewed mapping applied; financial cutover=%t\n", u.Id, entry.ImportBalance)
	}
	return nil
}

func main() {
	manifestPath := flag.String("manifest", "", "reviewed JSON account mapping")
	printDigest := flag.Bool("snapshot-digest", false, "print the canonical reviewed batch/user snapshot SHA256, without database access")
	apply := flag.Bool("apply", false, "apply reviewed mappings; default only validates local snapshots")
	databasePath := flag.String("sqlite-path", "", "existing SQLite database path when SQL_DSN is local")
	flag.Parse()
	if *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "-manifest is required")
		os.Exit(2)
	}
	file, err := os.Open(*manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot open reviewed manifest")
		os.Exit(1)
	}
	raw, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	_ = file.Close()
	if err != nil || len(raw) > 4<<20 {
		fmt.Fprintln(os.Stderr, "invalid manifest size")
		os.Exit(1)
	}
	var in migrationManifest
	if err = common.Unmarshal(raw, &in); err != nil {
		fmt.Fprintln(os.Stderr, "invalid manifest JSON")
		os.Exit(1)
	}
	if *printDigest {
		fmt.Println(manifestSnapshotDigest(in))
		return
	}
	if err = validateManifest(in); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if os.Getenv("SQL_DSN") == "" && *databasePath == "" {
		fmt.Fprintln(os.Stderr, "explicit existing SQL_DSN or -sqlite-path is required")
		os.Exit(2)
	}
	if *databasePath != "" {
		if _, err = os.Stat(*databasePath); err != nil {
			fmt.Fprintln(os.Stderr, "existing SQLite database is required")
			os.Exit(1)
		}
		common.SQLitePath = *databasePath
	}
	common.IsMasterNode = false // A dry run never seeds roles or migrates the schema.
	if err = model.InitDB(); err != nil {
		fmt.Fprintln(os.Stderr, "database unavailable")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err = run(ctx, in, *apply); err != nil {
		fmt.Fprintln(os.Stderr, "migration stopped:", err)
		os.Exit(1)
	}
}
