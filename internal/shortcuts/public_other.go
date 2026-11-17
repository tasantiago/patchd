//go:build !windows

package shortcuts

import "errors"

// PublicDesktop: só no Windows.
func PublicDesktop(string) (*PublicManager, error) {
	return nil, errors.New("atalhos na área de trabalho pública só no Windows")
}
