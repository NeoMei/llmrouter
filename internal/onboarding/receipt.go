package onboarding

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	receiptKeyTempPrefix     = ".receipt-key-"
	receiptStatusActive      = "active"
	receiptStatusRestoring   = "restoring"
	receiptStatusRestored    = "restored"
	receiptStatusUninstalled = "uninstalled"
)

type ReceiptMeta struct {
	Clients      []ClientID `json:"clients,omitempty"`
	Model        string     `json:"model,omitempty"`
	BridgeSHA256 string     `json:"bridge_sha256,omitempty"`
	ServerURL    string     `json:"server_url,omitempty"`
}

type ReceiptFile struct {
	Path           string `json:"path"`
	OriginalHash   string `json:"original_hash"`
	ConfiguredHash string `json:"configured_hash"`
	BackupPath     string `json:"backup_path"`
	Existed        bool   `json:"existed"`
}

type Receipt struct {
	ID             string        `json:"id"`
	Path           string        `json:"path"`
	Home           string        `json:"home"`
	CreatedAt      time.Time     `json:"created_at"`
	Meta           ReceiptMeta   `json:"meta"`
	Files          []ReceiptFile `json:"files"`
	Status         string        `json:"status"`
	RecoveryAction string        `json:"recovery_action,omitempty"`
	Integrity      string        `json:"integrity"`
}

func (tx *Transaction) Commit(meta ReceiptMeta) (Receipt, error) {
	if tx == nil || tx.committed {
		return Receipt{}, fmt.Errorf("%w: transaction is closed", ErrConflict)
	}
	if hasControl(meta.Model) {
		return Receipt{}, fmt.Errorf("%w: receipt metadata contains control characters", ErrUsage)
	}
	if meta.BridgeSHA256 != "" && !validSHA256(meta.BridgeSHA256) {
		return Receipt{}, fmt.Errorf("%w: receipt bridge checksum is invalid", ErrUsage)
	}
	files := tx.sortedFiles()
	if len(files) == 0 {
		return Receipt{}, fmt.Errorf("%w: transaction has no files", ErrUsage)
	}
	for _, file := range files {
		if err := snapshotMatches(tx.home, file); err != nil {
			return Receipt{}, err
		}
	}

	id, err := newReceiptID()
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: create receipt ID", ErrUnsafe)
	}
	receipt := Receipt{ID: id, Path: receiptPath(tx.home, id), Home: tx.home, CreatedAt: time.Now().UTC(), Meta: meta, Status: receiptStatusActive, Files: make([]ReceiptFile, 0, len(files))}
	for index, file := range files {
		receiptFile := ReceiptFile{Path: file.path, OriginalHash: file.originalHash, ConfiguredHash: digest(file.configured), BackupPath: canonicalBackupPath(tx.home, id, index), Existed: file.existed}
		if file.existed {
			if err := atomicWrite(tx.home, receiptFile.BackupPath, file.original); err != nil {
				return Receipt{}, err
			}
		}
		receipt.Files = append(receipt.Files, receiptFile)
	}
	if err := validateReceipt(receipt); err != nil {
		return Receipt{}, err
	}

	applied := make([]transactionFile, 0, len(files))
	for _, file := range files {
		if err := atomicWrite(tx.home, file.path, file.configured); err != nil {
			if rollbackErr := tx.rollback(applied); rollbackErr != nil {
				return Receipt{}, fmt.Errorf("%w: apply failed and rollback failed: %v", ErrUnsafe, rollbackErr)
			}
			return Receipt{}, err
		}
		applied = append(applied, file)
	}
	if err := persistReceipt(&receipt); err != nil {
		if rollbackErr := tx.rollback(applied); rollbackErr != nil {
			return Receipt{}, fmt.Errorf("%w: receipt persistence and rollback failed: %v", ErrUnsafe, rollbackErr)
		}
		return Receipt{}, err
	}
	tx.committed = true
	return receipt, nil
}

func (tx *Transaction) rollback(applied []transactionFile) error {
	var rollbackErr error
	for index := len(applied) - 1; index >= 0; index-- {
		file := applied[index]
		var err error
		if file.existed {
			err = atomicWrite(tx.home, file.path, file.original)
		} else {
			err = removeSecureFile(tx.home, file.path)
		}
		if err != nil && rollbackErr == nil {
			rollbackErr = err
		}
	}
	return rollbackErr
}

func snapshotMatches(home string, file transactionFile) error {
	current, err := secureReadFile(home, file.path)
	if file.existed {
		if err != nil || digest(current) != file.originalHash {
			return fmt.Errorf("%w: %s changed before configuration", ErrConflict, file.path)
		}
		return nil
	}
	if err == nil {
		return fmt.Errorf("%w: %s appeared before configuration", ErrConflict, file.path)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("%w: inspect destination", ErrUnsafe)
	}
	return nil
}

func Restore(home, receiptID string) error {
	return reverseReceipt(home, receiptID, receiptStatusRestored)
}

// Uninstall reverses a receipt and records a distinct terminal uninstall state.
func Uninstall(home, receiptID string) error {
	return reverseReceipt(home, receiptID, receiptStatusUninstalled)
}

type restoreFile struct {
	receipt    ReceiptFile
	configured []byte
	original   []byte
}

func reverseReceipt(home, receiptID, requestedAction string) error {
	receipt, err := loadReceipt(home, receiptID)
	if err != nil {
		return err
	}
	action := requestedAction
	resuming := false
	switch receipt.Status {
	case receiptStatusRestored, receiptStatusUninstalled:
		return nil
	case receiptStatusRestoring:
		if receipt.RecoveryAction != receiptStatusRestored && receipt.RecoveryAction != receiptStatusUninstalled {
			return fmt.Errorf("%w: receipt recovery action is invalid", ErrUnsafe)
		}
		action, resuming = receipt.RecoveryAction, true
	case receiptStatusActive:
	default:
		return fmt.Errorf("%w: invalid receipt state", ErrUnsafe)
	}

	staged := make([]restoreFile, 0, len(receipt.Files))
	for _, file := range receipt.Files {
		if err := validateSecurePath(receipt.Home, file.Path); err != nil {
			return err
		}
		stage := restoreFile{receipt: file}
		current, readErr := secureReadFile(receipt.Home, file.Path)
		if file.Existed {
			if err := validateSecurePath(receipt.Home, file.BackupPath); err != nil {
				return err
			}
			stage.original, err = secureReadFile(receipt.Home, file.BackupPath)
			if err != nil || digest(stage.original) != file.OriginalHash {
				return fmt.Errorf("%w: backup is unavailable", ErrUnsafe)
			}
		}
		switch {
		case readErr == nil && digest(current) == file.ConfiguredHash:
			stage.configured = current
		case file.Existed && readErr == nil && digest(current) == file.OriginalHash:
			if !resuming {
				return fmt.Errorf("%w: %s changed since configuration", ErrConflict, file.Path)
			}
			stage.configured = nil
		case !file.Existed && os.IsNotExist(readErr):
			if !resuming {
				return fmt.Errorf("%w: %s changed since configuration", ErrConflict, file.Path)
			}
			stage.configured = nil
		default:
			return fmt.Errorf("%w: %s changed since configuration", ErrConflict, file.Path)
		}
		staged = append(staged, stage)
	}

	if !resuming {
		receipt.Status = receiptStatusRestoring
		receipt.RecoveryAction = action
		if err := persistReceipt(&receipt); err != nil {
			return err
		}
	}
	applied := make([]restoreFile, 0, len(staged))
	for _, file := range staged {
		if file.configured == nil {
			continue
		}
		if file.receipt.Existed {
			err = atomicWrite(receipt.Home, file.receipt.Path, file.original)
		} else {
			err = removeSecureFile(receipt.Home, file.receipt.Path)
		}
		if err != nil {
			if rollbackErr := rollbackRestore(receipt.Home, applied); rollbackErr != nil {
				return fmt.Errorf("%w: restore failed and rollback failed: %v", ErrUnsafe, rollbackErr)
			}
			if !resuming {
				receipt.Status, receipt.RecoveryAction = receiptStatusActive, ""
				if stateErr := persistReceipt(&receipt); stateErr != nil {
					return fmt.Errorf("%w: restore rollback state is incomplete: %v", ErrUnsafe, stateErr)
				}
			}
			return err
		}
		applied = append(applied, file)
	}
	receipt.Status, receipt.RecoveryAction = action, ""
	if err := persistReceipt(&receipt); err != nil {
		return fmt.Errorf("%w: restored files are journaled for recovery", ErrUnsafe)
	}
	return nil
}

func rollbackRestore(home string, applied []restoreFile) error {
	var rollbackErr error
	for index := len(applied) - 1; index >= 0; index-- {
		if err := atomicWrite(home, applied[index].receipt.Path, applied[index].configured); err != nil && rollbackErr == nil {
			rollbackErr = err
		}
	}
	return rollbackErr
}

func ListReceipts(home string) ([]Receipt, error) {
	absolute, err := canonicalHome(home)
	if err != nil {
		return nil, err
	}
	if err := ensureStateRoots(absolute); err != nil {
		return nil, err
	}
	dir := filepath.Join(absolute, ".llmrouter", "receipts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: list receipts", ErrUnsafe)
	}
	receipts := make([]Receipt, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		receipt, err := loadReceipt(absolute, id)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].CreatedAt.After(receipts[j].CreatedAt) })
	return receipts, nil
}

func loadReceipt(home, receiptID string) (Receipt, error) {
	absolute, err := canonicalHome(home)
	if err != nil {
		return Receipt{}, err
	}
	if !validReceiptID(receiptID) {
		return Receipt{}, fmt.Errorf("%w: invalid receipt ID", ErrUsage)
	}
	path := receiptPath(filepath.Clean(absolute), receiptID)
	if err := validateSecurePath(filepath.Clean(absolute), path); err != nil {
		return Receipt{}, err
	}
	raw, err := secureReadFile(filepath.Clean(absolute), path)
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: receipt is unavailable", ErrUnsafe)
	}
	var receipt Receipt
	if err := common.Unmarshal(raw, &receipt); err != nil {
		return Receipt{}, fmt.Errorf("%w: invalid receipt", ErrUnsafe)
	}
	if receipt.ID != receiptID || filepath.Clean(receipt.Home) != filepath.Clean(absolute) || receipt.Path != path {
		return Receipt{}, fmt.Errorf("%w: receipt does not match home", ErrUnsafe)
	}
	if err := verifyReceiptIntegrity(receipt); err != nil {
		return Receipt{}, err
	}
	if err := validateReceipt(receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func validateReceipt(receipt Receipt) error {
	if len(receipt.Files) == 0 {
		return fmt.Errorf("%w: receipt has no files", ErrUnsafe)
	}
	seen := make(map[string]struct{}, len(receipt.Files))
	for index, file := range receipt.Files {
		if file.Path == "" || file.OriginalHash == "" || file.ConfiguredHash == "" || file.BackupPath == "" {
			return fmt.Errorf("%w: receipt file path is invalid", ErrUnsafe)
		}
		if err := validateWithinHome(receipt.Home, file.Path); err != nil || !isAllowedManagedDestination(receipt.Home, file.Path) {
			return fmt.Errorf("%w: receipt file path is invalid", ErrUnsafe)
		}
		if _, exists := seen[file.Path]; exists {
			return fmt.Errorf("%w: receipt has duplicate targets", ErrUnsafe)
		}
		seen[file.Path] = struct{}{}
		if file.BackupPath != canonicalBackupPath(receipt.Home, receipt.ID, index) {
			return fmt.Errorf("%w: receipt backup path is invalid", ErrUnsafe)
		}
	}
	return nil
}

// isAllowedManagedDestination is the Task 3 adapter boundary. Transactions accept
// only exact adapter-registered files beneath these client configuration roots.
func isAllowedManagedDestination(home, path string) bool {
	if err := validateWithinHome(home, path); err != nil {
		return false
	}
	rel, _ := filepath.Rel(home, path)
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == ".ssh" {
		return false
	}
	if parts[0] == ".llmrouter" {
		return len(parts) == 3 && parts[1] == "credentials" && parts[2] == "claude-desktop.json"
	}
	if len(parts) >= 3 && parts[len(parts)-2] == "Claude" && parts[len(parts)-1] == "claude_desktop_config.json" {
		return true
	}
	if len(parts) == 1 {
		return strings.HasPrefix(parts[0], ".")
	}
	for _, root := range []string{".config", ".continue", ".claude", ".codex", ".opencode", "Library"} {
		if parts[0] == root {
			return true
		}
	}
	return false
}

func receiptPath(home, id string) string {
	return filepath.Join(home, ".llmrouter", "receipts", id+".json")
}
func receiptBackupDir(home, id string) string {
	return filepath.Join(home, ".llmrouter", "backups", id)
}
func canonicalBackupPath(home, id string, index int) string {
	return filepath.Join(receiptBackupDir(home, id), fmt.Sprintf("%03d.backup", index))
}

func persistReceipt(receipt *Receipt) error {
	key, err := receiptKey(receipt.Home, true)
	if err != nil {
		return err
	}
	receipt.Integrity = receiptMAC(key, *receipt)
	raw, err := common.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("%w: encode receipt", ErrUnsafe)
	}
	return atomicWrite(receipt.Home, receipt.Path, raw)
}

func verifyReceiptIntegrity(receipt Receipt) error {
	key, err := receiptKey(receipt.Home, false)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(receipt.Integrity), []byte(receiptMAC(key, receipt))) {
		return fmt.Errorf("%w: receipt integrity check failed", ErrUnsafe)
	}
	return nil
}

func receiptMAC(key []byte, receipt Receipt) string {
	receipt.Integrity = ""
	payload, err := common.Marshal(receipt)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return base64.RawStdEncoding.EncodeToString(mac.Sum(nil))
}

func receiptKey(home string, create bool) ([]byte, error) {
	path := filepath.Join(home, ".llmrouter", "credentials", "receipt.key")
	key, err := loadPublishedReceiptKey(home, path)
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("%w: receipt key is invalid", ErrUnsafe)
		}
		return key, nil
	}
	if !create || !os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: receipt key is unavailable", ErrUnsafe)
	}
	candidate := make([]byte, 32)
	if _, err := rand.Read(candidate); err != nil {
		return nil, fmt.Errorf("%w: create receipt key", ErrUnsafe)
	}
	if _, err := publishReceiptKey(home, path, candidate); err != nil {
		return nil, err
	}
	key, err = loadPublishedReceiptKey(home, path)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%w: receipt key is unavailable", ErrUnsafe)
	}
	return key, nil
}

func newReceiptID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + fmt.Sprintf("%x", bytes), nil
}

func validReceiptID(id string) bool {
	if id == "" || strings.Contains(id, "..") {
		return false
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '.') {
			return false
		}
	}
	return true
}
