//go:build !windows

package platform

// WindowsInstallerBusy: fora do Windows, não há Windows Installer.
func WindowsInstallerBusy() (bool, error) { return false, nil }
