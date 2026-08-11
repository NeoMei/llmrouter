package onboarding

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type ReceiptMeta struct {
	Clients []ClientID `json:"clients,omitempty"`
	Model   string     `json:"model,omitempty"`
}

type ReceiptFile struct {
	Path           string `json:"path"`
	OriginalHash   string `json:"original_hash"`
	ConfiguredHash string `json:"configured_hash"`
	BackupPath     string `json:"backup_path"`
	Existed        bool   `json:"existed"`
}

type Receipt struct {
	ID        string        `json:"id"`
	Path      string        `json:"path"`
	Home      string        `json:"home"`
	CreatedAt time.Time     `json:"created_at"`
	Meta      ReceiptMeta   `json:"meta"`
	Files     []ReceiptFile `json:"files"`
}

func (tx *Transaction) Commit(meta ReceiptMeta) (Receipt, error) {
	if tx == nil || tx.committed {
		return Receipt{}, fmt.Errorf("%w: transaction is closed", ErrConflict)
	}
	if hasControl(meta.Model) {
		return Receipt{}, fmt.Errorf("%w: receipt metadata contains control characters", ErrUsage)
	}
	files := tx.sortedFiles()
	if len(files) == 0 {
		return Receipt{}, fmt.Errorf("%w: transaction has no files", ErrUsage)
	}
	for _, file := range files {
		if err := snapshotMatches(file); err != nil {
			return Receipt{}, err
		}
	}

	id, err := newReceiptID()
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: create receipt ID", ErrUnsafe)
	}
	backupDir := filepath.Join(tx.home, ".llmapi", "backups", id)
	if err := ensureSecureDirectory(tx.home, backupDir); err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{ID: id, Path: filepath.Join(tx.home, ".llmapi", "receipts", id+".json"), Home: tx.home, CreatedAt: time.Now().UTC(), Meta: meta, Files: make([]ReceiptFile, 0, len(files))}
	for index, file := range files {
		receiptFile := ReceiptFile{Path: file.path, OriginalHash: file.originalHash, ConfiguredHash: digest(file.configured), BackupPath: filepath.Join(backupDir, fmt.Sprintf("%03d.backup", index)), Existed: file.existed}
		if file.existed {
			if err := atomicWrite(tx.home, receiptFile.BackupPath, file.original); err != nil {
				return Receipt{}, err
			}
		}
		receipt.Files = append(receipt.Files, receiptFile)
	}

	applied := make([]transactionFile, 0, len(files))
	for _, file := range files {
		if err := atomicWrite(tx.home, file.path, file.configured); err != nil {
			tx.rollback(applied)
			return Receipt{}, err
		}
		applied = append(applied, file)
	}
	raw, err := common.Marshal(receipt)
	if err != nil {
		tx.rollback(applied)
		return Receipt{}, fmt.Errorf("%w: encode receipt", ErrUnsafe)
	}
	if err := atomicWrite(tx.home, receipt.Path, raw); err != nil {
		tx.rollback(applied)
		return Receipt{}, err
	}
	tx.committed = true
	return receipt, nil
}

func (tx *Transaction) rollback(applied []transactionFile) {
	for index := len(applied) - 1; index >= 0; index-- {
		file := applied[index]
		if file.existed {
			_ = atomicWrite(tx.home, file.path, file.original)
		} else {
			_ = removeSecureFile(tx.home, file.path)
		}
	}
}

func snapshotMatches(file transactionFile) error {
	current, err := os.ReadFile(file.path)
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
	receipt, err := loadReceipt(home, receiptID)
	if err != nil {
		return err
	}
	for _, file := range receipt.Files {
		if err := validateSecurePath(receipt.Home, file.Path); err != nil {
			return err
		}
		if file.Existed {
			if err := validateSecurePath(receipt.Home, file.BackupPath); err != nil {
				return err
			}
		}
		if err := currentConfigured(file); err != nil {
			return err
		}
	}
	for _, file := range receipt.Files {
		if file.Existed {
			backup, err := os.ReadFile(file.BackupPath)
			if err != nil || digest(backup) != file.OriginalHash {
				return fmt.Errorf("%w: backup is unavailable", ErrUnsafe)
			}
			if err := atomicWrite(receipt.Home, file.Path, backup); err != nil {
				return err
			}
		} else if err := removeSecureFile(receipt.Home, file.Path); err != nil {
			return err
		}
	}
	return nil
}

// Uninstall reverses the configuration associated with a receipt.
func Uninstall(home, receiptID string) error { return Restore(home, receiptID) }

func ListReceipts(home string) ([]Receipt, error) {
	absolute, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve home", ErrUnsafe)
	}
	if err := ensureStateRoots(filepath.Clean(absolute)); err != nil {
		return nil, err
	}
	dir := filepath.Join(absolute, ".llmapi", "receipts")
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
	absolute, err := filepath.Abs(home)
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: resolve home", ErrUnsafe)
	}
	if !validReceiptID(receiptID) {
		return Receipt{}, fmt.Errorf("%w: invalid receipt ID", ErrUsage)
	}
	path := filepath.Join(absolute, ".llmapi", "receipts", receiptID+".json")
	if err := validateSecurePath(filepath.Clean(absolute), path); err != nil {
		return Receipt{}, err
	}
	raw, err := os.ReadFile(path)
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
	if err := validateReceipt(receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func validateReceipt(receipt Receipt) error {
	if len(receipt.Files) == 0 {
		return fmt.Errorf("%w: receipt has no files", ErrUnsafe)
	}
	for _, file := range receipt.Files {
		if file.Path == "" || file.OriginalHash == "" || file.ConfiguredHash == "" || file.BackupPath == "" {
			return fmt.Errorf("%w: receipt file path is invalid", ErrUnsafe)
		}
		if err := validateWithinHome(receipt.Home, file.Path); err != nil {
			return fmt.Errorf("%w: receipt file path is invalid", ErrUnsafe)
		}
		if err := validateWithinHome(receipt.Home, file.BackupPath); err != nil {
			return fmt.Errorf("%w: receipt backup path is invalid", ErrUnsafe)
		}
	}
	return nil
}

func currentConfigured(file ReceiptFile) error {
	if file.Path == "" || file.ConfiguredHash == "" {
		return fmt.Errorf("%w: invalid receipt file", ErrUnsafe)
	}
	current, err := os.ReadFile(file.Path)
	if err != nil || digest(current) != file.ConfiguredHash {
		return fmt.Errorf("%w: %s changed since configuration", ErrConflict, file.Path)
	}
	return nil
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
