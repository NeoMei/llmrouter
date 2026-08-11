//go:build windows

package onboarding

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Windows operations fail closed until the installer supplies a current-user-only ACL implementation.
// Go's standard library does not expose the required ACL ownership primitives.
func validateSecureOwner(os.FileInfo) error {
	return fmt.Errorf("%w: Windows ACL enforcement is unavailable", ErrUnsafe)
}
func validateTrustedBridgeParentOwner(path string) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		return fmt.Errorf("%w: inspect Windows bridge parent owner", ErrUnsafe)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("%w: inspect Windows bridge parent owner", ErrUnsafe)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return fmt.Errorf("%w: inspect Windows current user", ErrUnsafe)
	}
	if !owner.Equals(user.User.Sid) && !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return fmt.Errorf("%w: Windows bridge parent owner is not trusted", ErrUnsafe)
	}
	return nil
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

func ensureStateRoots(string) error {
	return fmt.Errorf("%w: Windows ACL enforcement is unavailable", ErrUnsafe)
}
func setStateRootTestHook(func()) {}

func setReceiptKeyPublishTestHook(func() error)                   {}
func setReceiptKeyPostPublishTestHook(func(string, string) error) {}
func setReceiptKeyDirectorySyncTestHook(func())                   {}
func publishReceiptKey(string, string, []byte) (bool, error) {
	return false, fmt.Errorf("%w: Windows ACL enforcement is unavailable", ErrUnsafe)
}
func loadPublishedReceiptKey(string, string) ([]byte, error) {
	return nil, fmt.Errorf("%w: Windows ACL enforcement is unavailable", ErrUnsafe)
}
