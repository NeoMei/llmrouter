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
	inputHome string
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
	inputHome, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve home", ErrUnsafe)
	}
	absolute, err := canonicalHome(inputHome)
	if err != nil {
		return nil, err
	}
	tx := &Transaction{home: absolute, inputHome: filepath.Clean(inputHome), files: make(map[string]transactionFile)}
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
	rawPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%w: resolve destination", ErrUnsafe)
	}
	rawPath = filepath.Clean(rawPath)
	if err := validateWithinHome(tx.inputHome, rawPath); err != nil {
		return err
	}
	if err := rejectSymlinksBelow(tx.inputHome, rawPath); err != nil {
		return err
	}
	absolute, err := canonicalPath(rawPath)
	if err != nil {
		return err
	}
	if err := validateWithinHome(tx.home, absolute); err != nil {
		return err
	}
	if !isAllowedManagedDestination(tx.home, absolute) {
		return fmt.Errorf("%w: destination is not a managed destination", ErrUnsafe)
	}
	if err := validateSecurePath(tx.home, absolute); err != nil {
		return err
	}
	if _, exists := tx.files[absolute]; exists {
		file := tx.files[absolute]
		file.configured = append([]byte(nil), data...)
		tx.files[absolute] = file
		return nil
	}
	original, err := secureReadFile(tx.home, absolute)
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

func canonicalHome(home string) (string, error) {
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: home directory is unavailable", ErrUnsafe)
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("%w: resolve home links", ErrUnsafe)
	}
	resolved = filepath.Clean(resolved)
	if err := rejectSymlinksBelow(filepath.VolumeName(resolved)+string(filepath.Separator), resolved); err != nil {
		return "", err
	}
	if err := validateHome(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func canonicalPath(path string) (string, error) {
	path = filepath.Clean(path)
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		if _, err := os.Lstat(parent); err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(parent)
			if resolveErr != nil {
				return "", fmt.Errorf("%w: resolve destination links", ErrUnsafe)
			}
			rel, _ := filepath.Rel(parent, path)
			return filepath.Join(resolved, rel), nil
		}
		if parent == filepath.Dir(parent) {
			return "", fmt.Errorf("%w: destination parent is unavailable", ErrUnsafe)
		}
	}
}

func rejectSymlinksBelow(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: destination is outside home", ErrUnsafe)
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: inspect path component", ErrUnsafe)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: path component is a symlink", ErrUnsafe)
		}
	}
	return nil
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
