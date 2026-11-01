package localapi

import("context";"strings";"testing";"github.com/tasantiago/patchd/internal/protocol")

type standardBackend struct{backend;seen protocol.SelfServiceUser;reported bool}
func(b *standardBackend)Standardization(ctx context.Context,u protocol.SelfServiceUser)(protocol.StandardizationPlan,error){b.seen=u;return protocol.StandardizationPlan{Revision:strings.Repeat("a",64),Shortcuts:[]protocol.StandardShortcut{{Name:"Portal",Kind:"site",Target:"https://example.com"}}},nil}
func(b *standardBackend)StandardizationReport(ctx context.Context,u protocol.SelfServiceUser,r protocol.StandardizationResult)error{b.seen=u;b.reported=true;return nil}

func TestStandardizationUsesLocalIdentity(t *testing.T){
 b:=&standardBackend{};s,_,_:=servidor(b);token:=tokenDe(`CORP\Ana`,interativo,TokenGroup{SID:"group",Attrs:ligado,Name:`CORP\RH`})
 r,_:=chama(t,s,token,"{\"op\":\"standardization\"}\n");if !r.OK||r.Standardization==nil||b.seen.Account!=`CORP\Ana`||len(b.seen.Groups)!=1||b.seen.Groups[0]!=`CORP\RH`{t.Fatalf("identidade: %+v %+v",r,b.seen)}
 r,_=chama(t,s,token,"{\"op\":\"standardization\",\"account\":\"CORP\\\\Admin\",\"groups\":[\"CORP\\\\Financeiro\"]}\n");if r.OK{t.Fatal("aceitou identidade fornecida pelo aplicativo")}
 r,_=chama(t,s,token,"{\"op\":\"standardization_report\",\"standardization_result\":{\"revision\":\""+strings.Repeat("a",64)+"\",\"items\":[]}}\n");if !r.OK||!b.reported||b.seen.Account!=`CORP\Ana`{t.Fatalf("relato: %+v",r)}
}
