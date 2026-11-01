//go:build windows

package shortcuts

import("os";"path/filepath";"testing";"github.com/tasantiago/patchd/internal/protocol")

func TestWindowsShortcutWritesRealLink(t *testing.T){
 dir:=t.TempDir();link:=filepath.Join(dir,"Editor.lnk")
 s:=protocol.StandardShortcut{Name:"Editor",Kind:"aplicativo",OS:"windows",Target:`%SystemRoot%\System32\notepad.exe`}
 if err:=writeWindows(link,s);err!=nil{t.Fatal(err)};st,err:=os.Stat(link);if err!=nil||st.Size()==0{t.Fatalf("link vazio: %v %v",st,err)}
 s=protocol.StandardShortcut{Name:"Portal",Kind:"site",Target:"https://example.com"};if err:=writeWindows(filepath.Join(dir,"Portal.url"),s);err!=nil{t.Fatal(err)}
}
