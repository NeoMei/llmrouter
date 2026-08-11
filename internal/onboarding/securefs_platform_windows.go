//go:build windows

package onboarding

import "os"

// Go's standard library has no cross-version ACL API equivalent to POSIX owner checks.
// This no-op hook keeps the core buildable; an installer-owned ACL implementation is required before Windows credential writes.
func validateSecureOwner(os.FileInfo) error { return nil }
func secureFilePlatform(string) error       { return nil }
