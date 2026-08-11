//go:build !windows

package onboarding

import (
	"fmt"
	"os"
	"syscall"
)

func validateSecureOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%w: path is not owned by the current user", ErrUnsafe)
	}
	return nil
}

func secureFilePlatform(string) error { return nil }
