//go:build !windows

package credential

import "os"

// restrict deixa o arquivo legível só pelo dono (root, quando o agente roda como serviço).
func restrict(path string) error { return os.Chmod(path, 0o600) }
