//go:build !windows

package wintrust

// Supported diz se este sistema confere assinaturas Authenticode.
const Supported = false

// Verify só existe no Windows.
func Verify(path string) (Signature, error) { return Signature{}, ErrUnsupported }
