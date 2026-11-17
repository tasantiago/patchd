//go:build !windows

package shortcuts

import "errors"

// PublicDesktop: só no Windows.
func PublicDesktop(string) (*PublicManager, error) {
	return nil, errors.New("atalhos na área de trabalho pública só no Windows")
}

// CentralLink: só no Windows.
func CentralLink(string) (AppLink, error) {
	return AppLink{}, errors.New("atalho da Central só no Windows")
}
