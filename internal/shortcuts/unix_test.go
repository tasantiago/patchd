//go:build linux || darwin

package shortcuts

import("os";"path/filepath";"runtime";"strings";"testing";"github.com/tasantiago/patchd/internal/protocol")

func TestUnixShortcutFormat(t *testing.T){
 p:=filepath.Join(t.TempDir(),"atalho"+filepath.Ext(shortcutFile(protocol.StandardShortcut{Kind:"site"})))
 s:=protocol.StandardShortcut{Name:"Portal RH",Kind:"site",Target:"https://example.com/?a=1&b=2"}
 if err:=writeUnix(p,s);err!=nil{t.Fatal(err)};b,_:=os.ReadFile(p)
 if runtime.GOOS=="darwin"{if !strings.Contains(string(b),"&amp;")||!strings.Contains(string(b),"<key>URL</key>"){t.Fatalf("plist: %s",b)}}else if !strings.Contains(string(b),"Type=Link\nURL=https://example.com/?a=1&b=2"){t.Fatalf("desktop: %s",b)}
 if got:=xdgDesktop("/home/user",`XDG_DESKTOP_DIR="$HOME/Área de Trabalho"`);got!="/home/user/Área de Trabalho"{t.Fatal(got)}
 if got:=xdgDesktop("/home/user", "XDG_DESKTOP_DIR=\"$(touch /tmp/evil)\"");got!="/home/user/Desktop"{t.Fatal(got)}
}
