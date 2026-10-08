package domain

import "runtime"

// Platform is the operating system the manager is running on.
type Platform string

const (
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
	PlatformDarwin  Platform = "darwin"
	PlatformFreeBSD Platform = "freebsd"
)

// CurrentPlatform returns the platform of the running process.
func CurrentPlatform() Platform {
	return PlatformFromGOOS(runtime.GOOS)
}

// PlatformFromGOOS maps a GOOS value to a Platform. Unknown values are passed
// through so that the manager still runs, just without platform specific
// behaviour.
func PlatformFromGOOS(goos string) Platform {
	switch goos {
	case "windows":
		return PlatformWindows
	case "linux":
		return PlatformLinux
	case "darwin":
		return PlatformDarwin
	case "freebsd", "openbsd", "netbsd":
		return PlatformFreeBSD
	default:
		return Platform(goos)
	}
}

// IsWindows reports whether p is Windows.
func (p Platform) IsWindows() bool { return p == PlatformWindows }

// IsDarwin reports whether p is macOS.
func (p Platform) IsDarwin() bool { return p == PlatformDarwin }

// IsUnix reports whether p is a Unix-like platform.
func (p Platform) IsUnix() bool {
	switch p {
	case PlatformLinux, PlatformDarwin, PlatformFreeBSD:
		return true
	default:
		return false
	}
}

// CaseInsensitivePaths reports whether the platform's default filesystem
// treats paths case-insensitively. Windows always does; macOS usually does
// (APFS default) but can be configured case-sensitively, so callers should treat
// this as "probably yes" and not as a guarantee.
func (p Platform) CaseInsensitivePaths() bool {
	return p == PlatformWindows || p == PlatformDarwin
}
