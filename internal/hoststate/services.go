package hoststate

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

func collectRunningServices(ctx context.Context) (*protocol.RunningServices, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return runningServices(ctx, runtime.GOOS, platform.ExecRunner{Timeout: 10*time.Second})
}

func runningServices(ctx context.Context, family string, run platform.Runner) (*protocol.RunningServices, error) {
	var data []byte
	var err error
	switch family {
	case "windows":
		return windowsRunningServices(ctx)
	case "linux":
		data, err = run.Run(ctx, "/usr/bin/systemctl", "list-units", "--type=service", "--state=running", "--no-legend", "--no-pager", "--plain", "--full")
	case "darwin":
		data, err = run.Run(ctx, "/bin/launchctl", "list")
	default:
		return nil, fmt.Errorf("sistema sem suporte")
	}
	if err != nil {
		return nil, err
	}
	return parseRunningServices(family, string(data))
}

func parseRunningServices(family, data string) (*protocol.RunningServices, error) {
	names := []string{}
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch family {
		case "linux":
			if len(fields) < 4 || fields[1] != "loaded" || fields[2] != "active" || fields[3] != "running" || !strings.HasSuffix(fields[0], ".service") {
				return nil, fmt.Errorf("saída systemd inesperada")
			}
			names = append(names, fields[0])
		case "darwin":
			if len(fields) == 3 && fields[0] == "PID" && fields[2] == "Label" {
				continue
			}
			if len(fields) != 3 {
				return nil, fmt.Errorf("saída launchctl inesperada")
			}
			if fields[0] == "-" {
				continue
			}
			pid, err := strconv.Atoi(fields[0])
			if err != nil || pid <= 0 {
				return nil, fmt.Errorf("PID inválido")
			}
			names = append(names, fields[2])
		default:
			return nil, fmt.Errorf("sistema sem suporte")
		}
	}
	return serviceSnapshot(family, names)
}

func serviceSnapshot(family string, names []string) (*protocol.RunningServices, error) {
	slices.Sort(names)
	names = slices.Compact(names)
	s := &protocol.RunningServices{Family: family, Names: names}
	if err := s.Check(); err != nil {
		return nil, err
	}
	return s, nil
}
