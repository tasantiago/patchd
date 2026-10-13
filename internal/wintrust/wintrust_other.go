//go:build !windows

package wintrust

// Supported diz se este sistema confere assinaturas Authenticode.
const Supported = false

// VerifyChain só existe no Windows.
func VerifyChain(path string) (Signature, error) { return Signature{}, ErrUnsupported }
