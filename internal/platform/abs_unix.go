//go:build linux || darwin

package platform

import "strings"

// isAbs em sistemas Unix: o caminho começa na raiz.
func isAbs(p string) bool { return strings.HasPrefix(p, "/") }
