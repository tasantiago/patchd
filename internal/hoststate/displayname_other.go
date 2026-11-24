//go:build !windows

package hoststate

// DisplayName: fora do Windows, a Central não mostra o nome de exibição ("").
func DisplayName(string) string { return "" }
