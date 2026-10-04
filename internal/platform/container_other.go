//go:build !linux

package platform

// InContainer: containers de Linux não rodam o agente do Windows nem do macOS.
func InContainer() (bool, string) { return false, "" }
