package hoststate

import (
	"context"

	"github.com/tasantiago/patchd/internal/protocol"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func windowsRunningServices(ctx context.Context) (*protocol.RunningServices, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	defer m.Disconnect()
	list, err := m.ListServices()
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, name := range list {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s, err := m.OpenService(name)
		if err != nil {
			return nil, err
		}
		status, err := s.Query()
		s.Close()
		if err != nil {
			return nil, err
		}
		if status.State == svc.Running {
			names = append(names, name)
		}
	}
	return serviceSnapshot("windows", names)
}
