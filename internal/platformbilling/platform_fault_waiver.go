package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PlatformFaultWaiverDecision struct {
	CallID         string `gorm:"primaryKey;size:128"`
	CaseID         string `gorm:"uniqueIndex;size:128"`
	ApprovalSHA256 string `gorm:"size:64;not null"`
	ApprovalJSON   string `gorm:"not null"`
	ProviderState  string `gorm:"size:32;not null"`
	CreatedAt      time.Time
}

func (PlatformFaultWaiverDecision) TableName() string { return "platform_model_fault_waiver_decisions" }

// A separate append-only event records customer accounting completion without
// changing the original UNKNOWN evidence or resolving the supplier obligation.
type PlatformFaultWaiverEvent struct {
	CallID    string `gorm:"primaryKey;size:128"`
	Kind      string `gorm:"primaryKey;size:32"`
	Detail    string `gorm:"not null"`
	CreatedAt time.Time
}

func (PlatformFaultWaiverEvent) TableName() string { return "platform_model_fault_waiver_events" }

// InstallPlatformFaultWaiverSchema is an explicit offline operator operation.
// Normal adapter startup and recovery never install or use these tables.
func InstallPlatformFaultWaiverSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrInvalid
	}
	db = db.WithContext(ctx)
	if err := db.AutoMigrate(&PlatformFaultWaiverDecision{}, &PlatformFaultWaiverEvent{}); err != nil {
		return err
	}
	for _, table := range []string{"platform_model_fault_waiver_decisions", "platform_model_fault_waiver_events"} {
		switch db.Dialector.Name() {
		case "sqlite":
			for _, operation := range []string{"UPDATE", "DELETE"} {
				if err := db.Exec("CREATE TRIGGER IF NOT EXISTS " + table + "_" + strings.ToLower(operation) + " BEFORE " + operation + " ON " + table + " BEGIN SELECT RAISE(ABORT, 'immutable platform fault waiver'); END").Error; err != nil {
					return err
				}
			}
		case "postgres":
			if err := db.Exec("CREATE OR REPLACE FUNCTION platform_fault_waiver_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'immutable platform fault waiver'; END; $$").Error; err != nil {
				return err
			}
			var exists int64
			if err := db.Raw("SELECT count(*) FROM pg_trigger WHERE tgname = ? AND tgrelid = ?::regclass", table+"_immutable", table).Scan(&exists).Error; err != nil {
				return err
			}
			if exists == 0 {
				if err := db.Exec("CREATE TRIGGER " + table + "_immutable BEFORE UPDATE OR DELETE ON " + table + " FOR EACH ROW EXECUTE PROCEDURE platform_fault_waiver_immutable()").Error; err != nil {
					return err
				}
			}
		case "mysql":
			for _, operation := range []string{"UPDATE", "DELETE"} {
				name := table + "_" + strings.ToLower(operation)
				var exists int64
				if err := db.Raw("SELECT count(*) FROM information_schema.triggers WHERE trigger_schema = DATABASE() AND trigger_name = ?", name).Scan(&exists).Error; err != nil {
					return err
				}
				if exists == 0 {
					if err := db.Exec("CREATE TRIGGER " + name + " BEFORE " + operation + " ON " + table + " FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'immutable platform fault waiver'").Error; err != nil {
						return err
					}
				}
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}

type platformFaultWaiverApproval struct {
	Schema                 int             `json:"schema"`
	CaseID                 string          `json:"case_id"`
	OperatorID             string          `json:"operator_id"`
	Reason                 string          `json:"reason"`
	ApprovalStatement      string          `json:"approval_statement"`
	ApprovalSourceSHA256   string          `json:"approval_source_sha256"`
	CallID                 string          `json:"call_id"`
	ActorID                string          `json:"actor_id"`
	AccountID              string          `json:"account_id"`
	OriginProductID        string          `json:"origin_product_id"`
	InstanceID             string          `json:"instance_id"`
	Asset                  string          `json:"asset"`
	Held                   string          `json:"held"`
	ProviderState          string          `json:"provider_state"`
	OriginalEvidenceSHA256 string          `json:"original_evidence_sha256"`
	FollowupEvidenceSHA256 string          `json:"followup_evidence_sha256"`
	ExpectedCentralRow     json.RawMessage `json:"expected_central_row"`
	GatewayCall            Call            `json:"gateway_call"`
	GatewayAttempts        []Attempt       `json:"gateway_attempts"`
}

var waiverDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func parsePlatformFaultWaiver(raw []byte, digest string) (platformFaultWaiverApproval, error) {
	var p platformFaultWaiverApproval
	sum := sha256.Sum256(raw)
	if len(raw) < 1 || len(raw) > 4<<20 || !waiverDigest.MatchString(digest) || hex.EncodeToString(sum[:]) != digest || common.UnmarshalUnique(raw, &p) != nil {
		return p, ErrInvalid
	}
	// Full shape comparison rejects missing and unknown fields, including inside
	// the original Call and Attempt snapshots. These are forensic preconditions.
	canonical, err := common.Marshal(p)
	if err != nil || !waiverJSONEqual(raw, canonical) {
		return p, ErrInvalid
	}
	held, ok := new(big.Int).SetString(p.Held, 10)
	if p.Schema != 1 || !stableID.MatchString(p.CaseID) || !stableID.MatchString(p.OperatorID) || strings.TrimSpace(p.Reason) == "" || len(p.Reason) > 1024 || strings.TrimSpace(p.ApprovalStatement) == "" || len(p.ApprovalStatement) > 8192 || !waiverDigest.MatchString(p.ApprovalSourceSHA256) || !stableID.MatchString(p.CallID) || !stableID.MatchString(p.ActorID) || !stableID.MatchString(p.AccountID) || p.OriginProductID != "llmrouter" || !stableID.MatchString(p.InstanceID) || p.Asset != "CNY/6" || !ok || held.Sign() <= 0 || held.BitLen() > 63 || held.String() != p.Held || p.ProviderState != "PROVIDER_UNRESOLVED" || !waiverDigest.MatchString(p.OriginalEvidenceSHA256) || !waiverDigest.MatchString(p.FollowupEvidenceSHA256) {
		return p, ErrInvalid
	}
	r := p.GatewayCall
	if r.CallID != p.CallID || r.ActorID != p.ActorID || r.AccountID != p.AccountID || r.OriginProductID != p.OriginProductID || r.InstanceID != p.InstanceID || r.Asset != p.Asset || r.Amount != p.Held || r.FundingMode != "prepaid_cash" || r.FundingProductID != "llmrouter" || r.Status != "UNKNOWN" || r.Evidence != "" || r.Intent != "" || r.EvidenceConflict || (r.Charged != "" && r.Charged != "0") || len(p.GatewayAttempts) != 1 {
		return p, ErrInvalid
	}
	a := p.GatewayAttempts[0]
	if a.ID == 0 || a.ChannelID <= 0 || a.CallID != p.CallID || a.Status != "DISPATCHED" {
		return p, ErrInvalid
	}
	var central struct {
		ID        string          `json:"id"`
		AccountID string          `json:"account_id"`
		Snapshot  json.RawMessage `json:"snapshot"`
	}
	var remote RemoteCall
	var centralSnapshot map[string]any
	if common.UnmarshalUnique(p.ExpectedCentralRow, &central) != nil || central.ID != p.CallID || central.AccountID != p.AccountID || common.UnmarshalUnique(central.Snapshot, &remote) != nil || common.UnmarshalUnique(central.Snapshot, &centralSnapshot) != nil || !remote.matches(r) || remote.Status != "RESERVED" || remote.Held != p.Held || remote.Charged != "0" {
		return p, ErrInvalid
	}
	if centralSnapshot["task_id"] != r.TaskID || centralSnapshot["service_id"] != "llmrouter" {
		return p, ErrInvalid
	}
	for _, name := range []string{"usage", "provider_cost", "final_digest"} {
		if value, exists := centralSnapshot[name]; exists && value != nil && value != "" {
			return p, ErrInvalid
		}
	}
	return p, nil
}

// ValidatePlatformFaultWaiverApproval performs no database or remote access.
func ValidatePlatformFaultWaiverApproval(raw []byte, digest string) error {
	_, err := parsePlatformFaultWaiver(raw, digest)
	return err
}

func waiverJSONEqual(a, b []byte) bool {
	var x, y any
	return common.UnmarshalUnique(a, &x) == nil && common.UnmarshalUnique(b, &y) == nil && reflect.DeepEqual(x, y)
}

func waiverIntent(digest string) string {
	raw, _ := common.Marshal(map[string]string{"reason": "platform_fault_waiver:" + digest})
	return string(raw)
}

func waiverStagedCallMatches(original, current Call, digest string) bool {
	if current.Status != "WAIVER_PENDING" && current.Status != "RELEASED" {
		return false
	}
	expected := original
	expected.Status, expected.Intent, expected.UpdatedAt = current.Status, waiverIntent(digest), current.UpdatedAt
	if current.Status == "RELEASED" {
		expected.Charged = "0"
	}
	return waiverCallEqual(expected, current)
}

func waiverCallEqual(a, b Call) bool {
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	b.CreatedAt, b.UpdatedAt = b.CreatedAt.UTC(), b.UpdatedAt.UTC()
	return reflect.DeepEqual(a, b)
}

func waiverAttemptsEqual(a, b []Attempt) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		x.CreatedAt, x.UpdatedAt = x.CreatedAt.UTC(), x.UpdatedAt.UTC()
		y.CreatedAt, y.UpdatedAt = y.CreatedAt.UTC(), y.UpdatedAt.UTC()
		if !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

// A fresh query is required for every table: retaining a GORM query after Find
// also retains its table. model.lockForUpdate cannot be imported here because
// model already depends on platformbilling through relay/common.
func waiverLockedRead(tx *gorm.DB) *gorm.DB {
	q := tx.Session(&gorm.Session{NewDB: true})
	if tx.Dialector.Name() != "sqlite" {
		return q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return q
}

// PreparePlatformFaultWaiver freezes one explicitly approved cash waiver. It
// never submits a model request or a financial command. Dry-run does not write.
func PreparePlatformFaultWaiver(ctx context.Context, db *gorm.DB, raw []byte, digest string, apply bool) (Call, error) {
	p, err := parsePlatformFaultWaiver(raw, digest)
	if err != nil || db == nil {
		return Call{}, ErrInvalid
	}
	var out Call
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var decision PlatformFaultWaiverDecision
		err := tx.First(&decision, "call_id = ? OR case_id = ?", p.CallID, p.CaseID).Error
		if err == nil {
			if decision.CallID != p.CallID || decision.CaseID != p.CaseID || decision.ApprovalSHA256 != digest || decision.ApprovalJSON != string(raw) {
				return ErrConflict
			}
			var attempts []Attempt
			if err := waiverLockedRead(tx).Where("call_id = ?", p.CallID).Order("id").Find(&attempts).Error; err != nil {
				return err
			}
			if err := waiverLockedRead(tx).First(&out, "call_id = ?", p.CallID).Error; err != nil {
				return err
			}
			if !waiverAttemptsEqual(attempts, p.GatewayAttempts) || !waiverStagedCallMatches(p.GatewayCall, out, digest) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// Supplier response capture locks Attempt before Call. Match that order.
		var attempts []Attempt
		if err := waiverLockedRead(tx).Where("call_id = ?", p.CallID).Order("id").Find(&attempts).Error; err != nil {
			return err
		}
		if err := waiverLockedRead(tx).First(&out, "call_id = ?", p.CallID).Error; err != nil {
			return err
		}
		if !waiverCallEqual(out, p.GatewayCall) || !waiverAttemptsEqual(attempts, p.GatewayAttempts) {
			return ErrConflict
		}
		if tx.Migrator().HasTable(&knowledgeResultRow{}) {
			var count int64
			if err := tx.Model(&knowledgeResultRow{}).Where("call_id = ?", p.CallID).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return ErrConflict
			}
		}
		if !apply {
			return nil
		}
		decision = PlatformFaultWaiverDecision{CallID: p.CallID, CaseID: p.CaseID, ApprovalSHA256: digest, ApprovalJSON: string(raw), ProviderState: "PROVIDER_UNRESOLVED"}
		if err := tx.Create(&decision).Error; err != nil {
			return err
		}
		saved := tx.Model(&Call{}).Where("call_id = ? AND status = ? AND evidence = ? AND intent = ? AND evidence_conflict = ?", p.CallID, "UNKNOWN", "", "", false).Updates(map[string]any{"status": "WAIVER_PENDING", "intent": waiverIntent(digest)})
		if saved.Error != nil {
			return saved.Error
		}
		if saved.RowsAffected != 1 {
			return ErrConflict
		}
		out.Status, out.Intent = "WAIVER_PENDING", waiverIntent(digest)
		return nil
	})
	return out, err
}

// FinalizePlatformFaultWaiver performs only an authenticated central GET, then
// records RELEASED for the customer. Supplier evidence and attempts stay intact.
func FinalizePlatformFaultWaiver(ctx context.Context, db *gorm.DB, cfg Config, raw []byte, digest string) (Call, error) {
	p, err := parsePlatformFaultWaiver(raw, digest)
	if err != nil || db == nil {
		return Call{}, ErrInvalid
	}
	c, err := newClient(cfg)
	if err != nil {
		return Call{}, err
	}
	var central struct {
		Snapshot map[string]any `json:"snapshot"`
	}
	if common.UnmarshalUnique(p.ExpectedCentralRow, &central) != nil {
		return Call{}, ErrInvalid
	}
	finalRaw, _ := common.Marshal([]string{"release", "platform_fault_waiver:" + digest})
	finalHash := sha256.Sum256(finalRaw)
	central.Snapshot["status"], central.Snapshot["charged"], central.Snapshot["final_digest"] = "RELEASED", "0", hex.EncodeToString(finalHash[:])
	var remote map[string]any
	if err := c.do(ctx, http.MethodGet, "/internal/v1/calls/"+p.CallID, "", nil, &remote); err != nil {
		return Call{}, err
	}
	// The HTTP decoder uses ordinary JSON numbers. Normalize through the same
	// exact JSON representation before comparing the whole expected snapshot.
	expectedJSON, _ := common.Marshal(central.Snapshot)
	remoteJSON, _ := common.Marshal(remote)
	if !waiverJSONEqual(expectedJSON, remoteJSON) {
		return Call{}, ErrConflict
	}
	var out Call
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var decision PlatformFaultWaiverDecision
		if err := tx.First(&decision, "call_id = ?", p.CallID).Error; err != nil {
			return err
		}
		if decision.CaseID != p.CaseID || decision.ApprovalSHA256 != digest || decision.ApprovalJSON != string(raw) || decision.ProviderState != "PROVIDER_UNRESOLVED" {
			return ErrConflict
		}
		var attempts []Attempt
		if err := waiverLockedRead(tx).Where("call_id = ?", p.CallID).Order("id").Find(&attempts).Error; err != nil {
			return err
		}
		if err := waiverLockedRead(tx).First(&out, "call_id = ?", p.CallID).Error; err != nil {
			return err
		}
		if !waiverAttemptsEqual(attempts, p.GatewayAttempts) || !waiverStagedCallMatches(p.GatewayCall, out, digest) {
			return ErrConflict
		}
		if out.Status == "RELEASED" {
			return nil
		}
		event := PlatformFaultWaiverEvent{CallID: p.CallID, Kind: "customer_released", Detail: string(remoteJSON)}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		saved := tx.Model(&Call{}).Where("call_id = ? AND status = ? AND intent = ? AND evidence = ? AND evidence_conflict = ?", p.CallID, "WAIVER_PENDING", waiverIntent(digest), p.GatewayCall.Evidence, false).Updates(map[string]any{"status": "RELEASED", "charged": "0"})
		if saved.Error != nil {
			return saved.Error
		}
		if saved.RowsAffected != 1 {
			return ErrConflict
		}
		out.Status, out.Charged = "RELEASED", "0"
		return nil
	})
	return out, err
}
