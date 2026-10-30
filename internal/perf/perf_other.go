//go:build !linux && !windows && !darwin

package perf

import "context"

func readCPU(context.Context) (cpuTimes, error) { return cpuTimes{}, ErrUnsupported }
func readMem(context.Context) (float64, error)  { return 0, ErrUnsupported }
