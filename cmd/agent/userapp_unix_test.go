//go:build linux || darwin

package main

import("encoding/xml";"strings";"testing")

func TestUnixAutostartEscapesExecutable(t *testing.T){
 exe:=`/opt/app com espaço/a&b/patchd-agent`
 b,err:=unixAutostart(exe,"darwin");if err!=nil{t.Fatal(err)}
 var doc struct{Dict struct{Args []string `xml:"array>string"`} `xml:"dict"`};if err:=xml.Unmarshal(b,&doc);err!=nil{t.Fatal(err)}
 if len(doc.Dict.Args)!=3||doc.Dict.Args[0]!=exe||doc.Dict.Args[1]!="app"||doc.Dict.Args[2]!="-no-tray"{t.Fatalf("argumentos: %+v",doc)}
 b,err=unixAutostart(exe,"linux");if err!=nil||!strings.Contains(string(b),`Exec="`+exe+`" app -no-tray`){t.Fatalf("XDG: %s %v",b,err)}
 if _,err:=unixAutostart("/tmp/app\nExec=evil","linux");err==nil{t.Fatal("aceitou injeção na configuração")}
}
