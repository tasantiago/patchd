//go:build !windows

package platform

// Processes: fora do Windows, o agente não precisa (o job thirdparty é só do Windows).
func Processes() ([]Process, error) { return nil, ErrNoProcessList }
