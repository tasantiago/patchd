package shortcuts

import("encoding/json";"os";"path/filepath";"strings";"testing";"github.com/tasantiago/patchd/internal/protocol")

func shortcutFixture(t *testing.T)(*Manager,protocol.StandardizationPlan){
 t.Helper();dir:=t.TempDir();m:=&Manager{Desktop:dir,StatePath:filepath.Join(t.TempDir(),"state.json"),Write:func(path string,s protocol.StandardShortcut)error{return os.WriteFile(path,[]byte(s.Target),0600)}}
 p:=protocol.StandardizationPlan{Revision:strings.Repeat("a",64),Shortcuts:[]protocol.StandardShortcut{{Name:"Portal",Kind:"site",Target:"https://example.com"}}};return m,p
}

func TestManagedShortcutLifecyclePreservesPersonalFiles(t *testing.T){
 m,p:=shortcutFixture(t);file:=filepath.Join(m.Desktop,shortcutFile(p.Shortcuts[0]))
 r,err:=m.Apply(p);if err!=nil||r.Items[0].State!="aplicado"{t.Fatalf("criar: %+v %v",r,err)}
 r,err=m.Apply(p);if err!=nil||r.Items[0].State!="em_dia"{t.Fatalf("repetir: %+v %v",r,err)}
 p.Shortcuts[0].Target="https://updated.example";r,err=m.Apply(p);if err!=nil||r.Items[0].State!="aplicado"{t.Fatalf("atualizar: %+v %v",r,err)}
 if err:=os.WriteFile(file,[]byte("pessoal"),0600);err!=nil{t.Fatal(err)}
 r,err=m.Apply(p);if err!=nil||r.Items[0].State!="conflito"{t.Fatalf("alterado: %+v %v",r,err)}
 p.Shortcuts=nil;if _,err=m.Apply(p);err!=nil{t.Fatal(err)};b,_:=os.ReadFile(file);if string(b)!="pessoal"{t.Fatal("apagou arquivo alterado pelo usuário")}
 m2,p2:=shortcutFixture(t);r,err=m2.Apply(p2);if err!=nil{t.Fatal(err)};ownedFile:=filepath.Join(m2.Desktop,shortcutFile(p2.Shortcuts[0]));p2.Shortcuts=nil;r,err=m2.Apply(p2);if err!=nil||r.Items[0].State!="removido"{t.Fatalf("remover: %+v %v",r,err)};if _,err:=os.Stat(ownedFile);!os.IsNotExist(err){t.Fatal("atalho próprio não removido")}
}

func TestExistingFileAndCorruptOwnershipArePreserved(t *testing.T){
 m,p:=shortcutFixture(t);file:=filepath.Join(m.Desktop,shortcutFile(p.Shortcuts[0]));if err:=os.WriteFile(file,[]byte("original"),0600);err!=nil{t.Fatal(err)}
 r,err:=m.Apply(p);if err!=nil||r.Items[0].State!="conflito"{t.Fatalf("arquivo pessoal: %+v %v",r,err)}
 bad:=manifest{Version:1,Entries:map[string]owned{"bad":{File:"../fora.url"}}};b,_:=json.Marshal(bad);if err:=os.WriteFile(m.StatePath,b,0600);err!=nil{t.Fatal(err)};if _,err:=m.Apply(p);err==nil{t.Fatal("aceitou caminho externo no controle")};b,_=os.ReadFile(file);if string(b)!="original"{t.Fatal("arquivo pessoal mudou")}
}
