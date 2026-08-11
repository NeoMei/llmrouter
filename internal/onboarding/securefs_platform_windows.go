//go:build windows

package onboarding

import (
	"fmt"
	"os"
)

// Windows operations fail closed until the installer supplies a current-user-only ACL implementation.
// Go's standard library does not expose the required ACL ownership primitives.
func validateSecureOwner(os.FileInfo) error {
	return fmt.Errorf("%w: Windows ACL enforcement is unavailable", ErrUnsafe)
}
func secureReadFile(string, string) ([]byte, error) {
	return nil, fmt.Errorf("%w: Windows ACL enforcement is unavailable", ErrUnsafe)
}
func atomicWrite(string, string, []byte) error {
	return fmt.Errorf("%w: Windows ACL enforcement is unavailable", ErrUnsafe)
}
func removeSecureFile(string, string) error {
	return fmt.Errorf("%w: Windows ACL enforcement is unavailable", ErrUnsafe)
}

func setSecureFSTestHook(func()) {}
