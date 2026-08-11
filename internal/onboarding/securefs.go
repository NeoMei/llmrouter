package onboarding

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const privateFileMode = 0o600
const privateDirMode = 0o700

type Transaction struct {
	home      string
	files     map[string]transactionFile
	committed bool
}

type transactionFile struct {
	path         string
	original     []byte
	existed      bool
	originalHash string
	configured   []byte
}

func BeginTransaction(home string) (*Transaction, error) {
	absolute, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve home", ErrUnsafe)
	}
	if err := validateHome(absolute); err != nil {
		return nil, err
	}
	tx := &Transaction{home: filepath.Clean(absolute), files: make(map[string]transactionFile)}
	if err := ensureStateRoots(tx.home); err != nil {
		return nil, err
	}
	return tx, nil
}

// Write stages a private, atomic replacement that is applied by Commit.
func (tx *Transaction) Write(path string, data []byte) error {
	if tx == nil || tx.committed {
		return fmt.Errorf("%w: transaction is closed", ErrConflict)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%w: resolve destination", ErrUnsafe)
	}
	absolute = filepath.Clean(absolute)
	if err := validateSecurePath(tx.home, absolute); err != nil {
		return err
	}
	if _, exists := tx.files[absolute]; exists {
		file := tx.files[absolute]
		file.configured = append([]byte(nil), data...)
		tx.files[absolute] = file
		return nil
	}
	original, err := os.ReadFile(absolute)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: read destination", ErrUnsafe)
	}
	tx.files[absolute] = transactionFile{
		path: absolute, original: append([]byte(nil), original...), existed: existed,
		originalHash: digest(original), configured: append([]byte(nil), data...),
	}
	return nil
}

func (tx *Transaction) sortedFiles() []transactionFile {
	files := make([]transactionFile, 0, len(tx.files))
	for _, file := range tx.files {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func ensureStateRoots(home string) error {
	if err := validateHome(home); err != nil {
		return err
	}
	for _, root := range []string{filepath.Join(home, ".llmapi"), filepath.Join(home, ".llmapi", "receipts"), filepath.Join(home, ".llmapi", "backups"), filepath.Join(home, ".llmapi", "credentials")} {
		if err := ensureSecureDirectory(home, root); err != nil {
			return err
		}
		if err := os.Chmod(root, privateDirMode); err != nil {
			return fmt.Errorf("%w: protect state directory", ErrUnsafe)
		}
	}
	return nil
}

func ensureSecureDirectory(home, directory string) error {
	if err := validateWithinHome(home, directory); err != nil {
		return err
	}
	rel, _ := filepath.Rel(home, directory)
	current := home
	for _, segment := range strings.Split(rel, string(filepath.Separator)) {
		if segment == "." || segment == "" {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, privateDirMode); err != nil && !os.IsExist(err) {
				return fmt.Errorf("%w: create private directory", ErrUnsafe)
			}
			info, err = os.Lstat(current)
		}
		if err != nil || !info.IsDir() {
			return fmt.Errorf("%w: state directory is invalid", ErrUnsafe)
		}
		if err := validateSecureOwner(info); err != nil {
			return err
		}
	}
	return nil
}

func validateSecurePath(home, path string) error {
	if err := validateHome(home); err != nil {
		return err
	}
	if err := validateWithinHome(home, path); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err := ensureSecureDirectory(home, parent); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect destination", ErrUnsafe)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: destination is a symlink", ErrUnsafe)
	}
	if info.IsDir() {
		return fmt.Errorf("%w: destination is a directory", ErrUnsafe)
	}
	return validateSecureOwner(info)
}

func validateHome(home string) error {
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: home directory is unavailable", ErrUnsafe)
	}
	return validateSecureOwner(info)
}

func validateWithinHome(home, path string) error {
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: destination is outside home", ErrUnsafe)
	}
	return nil
}

func atomicWrite(home, path string, data []byte) error {
	if err := validateSecurePath(home, path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".llmapi-")
	if err != nil {
		return fmt.Errorf("%w: create private temporary file", ErrUnsafe)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(privateFileMode); err != nil {
		temp.Close()
		return fmt.Errorf("%w: protect temporary file", ErrUnsafe)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("%w: write temporary file", ErrUnsafe)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("%w: sync temporary file", ErrUnsafe)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("%w: close temporary file", ErrUnsafe)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("%w: atomically replace destination", ErrUnsafe)
	}
	if err := secureFilePlatform(path); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	return nil
}

func removeSecureFile(home, path string) error {
	if err := validateSecurePath(home, path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: remove destination", ErrUnsafe)
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open directory for sync", ErrUnsafe)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("%w: sync directory", ErrUnsafe)
	}
	return nil
}
