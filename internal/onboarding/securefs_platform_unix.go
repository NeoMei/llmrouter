//go:build !windows

package onboarding

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var secureFSTestHook func()

func setSecureFSTestHook(hook func()) { secureFSTestHook = hook }

func validateSecureOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%w: path is not owned by the current user", ErrUnsafe)
	}
	return nil
}

// secureParent is an open, no-follow parent directory held through the operation.
type secureParent struct {
	fd      int
	name    string
	dirPath string
}

func (parent *secureParent) close() { _ = unix.Close(parent.fd) }

func openSecureParent(home, path string, create bool) (*secureParent, error) {
	if err := validateWithinHome(home, path); err != nil {
		return nil, err
	}
	// O_NOFOLLOW protects the supplied home itself; every descendant is opened relative
	// to the preceding held descriptor, so a parent swap cannot redirect the operation.
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open home without following links", ErrUnsafe)
	}
	closeFD := true
	defer func() {
		if closeFD {
			_ = unix.Close(fd)
		}
	}()
	if err := verifyDirectoryFD(fd); err != nil {
		return nil, err
	}

	rel, _ := filepath.Rel(home, filepath.Dir(path))
	for _, segment := range strings.Split(rel, string(filepath.Separator)) {
		if segment == "." || segment == "" {
			continue
		}
		next, openErr := unix.Openat(fd, segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if err := unix.Mkdirat(fd, segment, privateDirMode); err != nil && !errors.Is(err, unix.EEXIST) {
				return nil, fmt.Errorf("%w: create private directory", ErrUnsafe)
			}
			next, openErr = unix.Openat(fd, segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if errors.Is(openErr, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		if openErr != nil {
			return nil, fmt.Errorf("%w: open parent without following links", ErrUnsafe)
		}
		_ = unix.Close(fd)
		fd = next
		if err := verifyDirectoryFD(fd); err != nil {
			return nil, err
		}
	}
	if secureFSTestHook != nil {
		secureFSTestHook()
	}
	closeFD = false
	return &secureParent{fd: fd, name: filepath.Base(path), dirPath: filepath.Dir(path)}, nil
}

func verifyDirectoryFD(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%w: unsafe directory ownership", ErrUnsafe)
	}
	return nil
}

func verifyExistingFile(parent *secureParent) (bool, error) {
	fd, err := unix.Openat(parent.fd, parent.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: open destination without following links", ErrUnsafe)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT == unix.S_IFDIR || int(stat.Uid) != os.Getuid() {
		return false, fmt.Errorf("%w: unsafe destination", ErrUnsafe)
	}
	return true, nil
}

func secureReadFile(home, path string) ([]byte, error) {
	parent, err := openSecureParent(home, path, false)
	if err != nil {
		return nil, err
	}
	defer parent.close()
	fd, err := unix.Openat(parent.fd, parent.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), parent.name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("%w: invalid file", ErrUnsafe)
	}
	if err := validateSecureOwner(info); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func atomicWrite(home, path string, data []byte) error {
	parent, err := openSecureParent(home, path, true)
	if err != nil {
		return err
	}
	defer parent.close()
	if _, err := verifyExistingFile(parent); err != nil {
		return err
	}
	var tempName string
	var tempFD int
	for attempts := 0; attempts < 16; attempts++ {
		bytes := make([]byte, 8)
		if _, err := rand.Read(bytes); err != nil {
			return fmt.Errorf("%w: random temporary name", ErrUnsafe)
		}
		tempName = fmt.Sprintf(".llmapi-%x", bytes)
		tempFD, err = unix.Openat(parent.fd, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, privateFileMode)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		break
	}
	if err != nil {
		return fmt.Errorf("%w: create private temporary file", ErrUnsafe)
	}
	defer unix.Unlinkat(parent.fd, tempName, 0)
	file := os.NewFile(uintptr(tempFD), tempName)
	if err := unix.Fchmod(tempFD, privateFileMode); err != nil {
		file.Close()
		return fmt.Errorf("%w: protect temporary file", ErrUnsafe)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("%w: write temporary file", ErrUnsafe)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("%w: sync temporary file", ErrUnsafe)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: close temporary file", ErrUnsafe)
	}
	if err := unix.Renameat(parent.fd, tempName, parent.fd, parent.name); err != nil {
		return fmt.Errorf("%w: atomically replace destination", ErrUnsafe)
	}
	if err := unix.Fsync(parent.fd); err != nil {
		return fmt.Errorf("%w: sync directory", ErrUnsafe)
	}
	return nil
}

func removeSecureFile(home, path string) error {
	parent, err := openSecureParent(home, path, false)
	if err != nil {
		return err
	}
	defer parent.close()
	exists, err := verifyExistingFile(parent)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := unix.Unlinkat(parent.fd, parent.name, 0); err != nil {
		return fmt.Errorf("%w: remove destination", ErrUnsafe)
	}
	if err := unix.Fsync(parent.fd); err != nil {
		return fmt.Errorf("%w: sync directory", ErrUnsafe)
	}
	return nil
}

var stateRootTestHook func()

func setStateRootTestHook(hook func()) { stateRootTestHook = hook }

func ensureStateRoots(home string) error {
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%w: open home for state root", ErrUnsafe)
	}
	defer unix.Close(fd)
	if err := verifyDirectoryFD(fd); err != nil {
		return err
	}
	if stateRootTestHook != nil {
		stateRootTestHook()
	}
	state, err := openPrivateDirectoryAt(fd, ".llmapi")
	if err != nil {
		return fmt.Errorf("%w: state root", ErrUnsafe)
	}
	defer unix.Close(state)
	for _, name := range []string{"receipts", "backups", "credentials"} {
		child, err := openPrivateDirectoryAt(state, name)
		if err != nil {
			return fmt.Errorf("%w: state root", ErrUnsafe)
		}
		_ = unix.Close(child)
	}
	if err := unix.Fsync(state); err != nil {
		return fmt.Errorf("%w: sync state root", ErrUnsafe)
	}
	return unix.Fsync(fd)
}

func openPrivateDirectoryAt(parent int, name string) (int, error) {
	if err := unix.Mkdirat(parent, name, privateDirMode); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, err
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := unix.Fchmod(fd, privateDirMode); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if err := verifyDirectoryFD(fd); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if err := unix.Fsync(fd); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

var receiptKeyPublishTestHook func() error

func setReceiptKeyPublishTestHook(hook func() error) { receiptKeyPublishTestHook = hook }

// publishReceiptKey prepares a complete private temp inode, then links it into the
// final name. linkat fails when another creator has already published the key.
func publishReceiptKey(home, path string, key []byte) (bool, error) {
	parent, err := openSecureParent(home, path, false)
	if err != nil {
		return false, err
	}
	defer parent.close()
	if err := cleanupStaleReceiptKeyTemps(parent); err != nil {
		return false, err
	}
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return false, fmt.Errorf("%w: random receipt key temporary name", ErrUnsafe)
	}
	tempName := fmt.Sprintf("%s%x.tmp", receiptKeyTempPrefix, bytes)
	fd, err := unix.Openat(parent.fd, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, privateFileMode)
	if err != nil {
		return false, fmt.Errorf("%w: create receipt key temporary file", ErrUnsafe)
	}
	defer unix.Unlinkat(parent.fd, tempName, 0)
	file := os.NewFile(uintptr(fd), tempName)
	if err := unix.Fchmod(fd, privateFileMode); err != nil {
		file.Close()
		return false, fmt.Errorf("%w: protect receipt key temporary file", ErrUnsafe)
	}
	written, err := file.Write(key)
	if err != nil || written != len(key) {
		file.Close()
		return false, fmt.Errorf("%w: write receipt key temporary file", ErrUnsafe)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return false, fmt.Errorf("%w: sync receipt key temporary file", ErrUnsafe)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("%w: close receipt key temporary file", ErrUnsafe)
	}
	if receiptKeyPublishTestHook != nil {
		if err := receiptKeyPublishTestHook(); err != nil {
			return false, err
		}
	}
	if err := unix.Linkat(parent.fd, tempName, parent.fd, parent.name, 0); errors.Is(err, unix.EEXIST) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("%w: publish receipt key", ErrUnsafe)
	}
	if err := unix.Fsync(parent.fd); err != nil {
		return false, fmt.Errorf("%w: sync receipt key directory", ErrUnsafe)
	}
	return true, nil
}

func cleanupStaleReceiptKeyTemps(parent *secureParent) error {
	copyFD, err := unix.Dup(parent.fd)
	if err != nil {
		return fmt.Errorf("%w: inspect receipt key temporary files", ErrUnsafe)
	}
	dir := os.NewFile(uintptr(copyFD), parent.dirPath)
	entries, err := dir.ReadDir(-1)
	if err != nil {
		_ = dir.Close()
		return fmt.Errorf("%w: inspect receipt key temporary files", ErrUnsafe)
	}
	defer dir.Close()
	cutoff := time.Now().Add(-time.Hour)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), receiptKeyTempPrefix) || !strings.HasSuffix(entry.Name(), ".tmp") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().After(cutoff) || validateSecureOwner(info) != nil {
			continue
		}
		if err := unix.Unlinkat(parent.fd, entry.Name(), 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("%w: remove stale receipt key temporary file", ErrUnsafe)
		}
	}
	return nil
}
