package platform

import "path/filepath"

// isAbs no Windows exige letra de unidade ou caminho UNC (ex.: C:\Windows\System32\...).
func isAbs(p string) bool { return filepath.IsAbs(p) }
