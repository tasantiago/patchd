package shield

import (
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestAntivirusServices(t *testing.T) {
	p := AntivirusServices{Windows: []string{"A", "B", "C"}, Linux: []string{"av.service"}, MacOS: []string{"com.vendor.av"}}
	now := time.Now()
	old := now.Add(-49*time.Hour)
	for _, tc := range []struct {
		family string
		names []string
		at *time.Time
		want int
	}{
		{"windows", []string{"a","B","C"}, &now, 1},
		{"windows", []string{"A","B"}, &now, 0},
		{"linux", []string{"av.service"}, &now, 1},
		{"darwin", []string{"com.vendor.av"}, &now, 1},
		{"linux", []string{}, &now, 0},
		{"linux", []string{"av.service"}, &old, -1},
		{"linux", nil, &now, -1},
	} {
		got, detail := p.Evaluate(&protocol.RunningServices{Family:tc.family, Names:tc.names}, tc.at, now)
		n := -1
		if got != nil { n = 0; if *got { n = 1 } }
		if n != tc.want { t.Errorf("%+v: got %d (%s)", tc, n, detail) }
	}
}
