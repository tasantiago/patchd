package hoststate

import "testing"

func TestParseRunningServices(t *testing.T) {
	for _, tc := range []struct{family,data,name string}{
		{"linux", "av.service loaded active running Antivirus\n", "av.service"},
		{"darwin", "PID\tStatus\tLabel\n42\t0\tcom.vendor.av\n-\t0\tcom.vendor.stopped\n", "com.vendor.av"},
	} {
		s,err:=parseRunningServices(tc.family,tc.data)
		if err!=nil || s==nil || len(s.Names)!=1 || s.Names[0]!=tc.name { t.Fatalf("%s: %+v %v",tc.family,s,err) }
	}
	if _,err:=parseRunningServices("linux","unexpected");err==nil {t.Fatal("expected parse error")}
}
