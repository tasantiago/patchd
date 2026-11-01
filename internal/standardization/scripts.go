package standardization

import (
 "context"
 "crypto/ed25519"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "io"
 "os"
 "path/filepath"
 "slices"
 "strings"
 "time"

 "github.com/tasantiago/patchd/internal/jobs"
 "github.com/tasantiago/patchd/internal/protocol"
 "github.com/tasantiago/patchd/internal/store"
)

func resolveInstallers(policies []store.StandardizationPolicy,machine string,user protocol.SelfServiceUser,platform string,out *protocol.StandardizationPlan)[]store.StandardInstaller {
 byName:=map[string]store.StandardInstaller{};conflict:=map[string]bool{}
 for _,p:=range policies {if !Matches(p,machine,user){continue};for _,i:=range p.Installers {
  if i.OS!=platform{continue};key:=strings.ToLower(i.Software)
  if old,ok:=byName[key];ok&&store.StandardScriptKey(old.Params)!=store.StandardScriptKey(i.Params){conflict[key]=true};byName[key]=i
 }}
 keys:=[]string{};for k:=range byName{keys=append(keys,k)};slices.Sort(keys);outList:=[]store.StandardInstaller{}
 for _,k:=range keys{if conflict[k]{out.Warnings=append(out.Warnings,"Instaladores conflitantes para "+byName[k].Software);continue};if len(outList)+len(out.Apps)>=protocol.MaxStandardizationApps{out.Warnings=append(out.Warnings,"Limite de aplicativos resultantes atingido.");break};outList=append(outList,byName[k])}
 return outList
}

func(s *Service)scriptApplications(ctx context.Context,machine string,installers []store.StandardInstaller,inv protocol.InventoryReport,now time.Time,budget int)([]protocol.StandardAppState,int,error){
 latest,open,err:=s.Store.StandardizationJobs(ctx,machine,now);if err!=nil{return nil,0,err}
 out:=[]protocol.StandardAppState{};created:=0
 for _,i:=range installers {
  key:=store.StandardScriptKey(i.Params);state:=protocol.StandardAppState{App:key,Name:i.Software,State:"bloqueado"}
  switch {
  case i.Validate()!=nil:state.Detail="Configuração de instalação inválida; salve novamente o grupo."
  case slices.ContainsFunc(inv.Software,func(sw protocol.Software)bool{return strings.EqualFold(sw.Name,i.Software)&&sw.Scope!="user"&&!sw.SystemComponent&&!sw.IsUpdate}):state.State="em_dia";state.Detail="Presença confirmada no inventário; este padrão não exige versão específica."
  default:
   last,had:=latest[key]
   switch {
   case had&&!jobs.Final(last.State):state.State="em_andamento";state.JobID=last.ID;state.Detail="Instalador aguardando execução ou resultado."
   case had&&last.State==jobs.StateDone&&last.FinishedAt!=nil&&now.Sub(*last.FinishedAt)<RetryAfter:
    state.State="falhou";state.JobID=last.ID;state.Detail="Script concluído, mas o aplicativo não aparece no inventário. Confira o nome de detecção e o instalador antes da próxima tentativa em seis horas."
   case had&&last.FinishedAt!=nil&&now.Sub(*last.FinishedAt)<RetryAfter&&last.State!=jobs.StateCanceled:
    state.State="falhou";state.JobID=last.ID;state.Detail="Tentativa sem sucesso; nova avaliação após seis horas."
   case open>0||created>=budget:state.State="aguardando";state.Detail="Aguardando a fila de instalações do computador."
   case len(s.Key)!=ed25519.PrivateKeySize||s.BundlesDir=="":state.Detail="Chave de jobs ou repositório de scripts não configurado."
   default:
    if !verifiedBundle(s.BundlesDir,i.Params){state.Detail="Pacote de instalação ausente ou diferente do aprovado no grupo.";break}
    if err:=ctx.Err();err!=nil{return nil,created,err}
    raw,err:=json.Marshal(i.Params);if err!=nil{return nil,created,err}
    rec,err:=jobs.Issue(ctx,s.Store,s.Key,jobs.Request{MachineID:machine,Type:jobs.TypeScript,TTL:24*time.Hour,By:store.StandardizationAuthor,Params:raw},now);if err!=nil{return nil,created,err}
    if s.OnCreated!=nil{s.OnCreated(rec,i.Software,i.Params.SHA256[:12])};created++;state.State="agendado";state.JobID=rec.ID;state.Detail="Instalador assinado agendado; a presença será confirmada pelo inventário posterior."
   }
  }
  out=append(out,state)
 }
 return out,created,nil
}

func verifiedBundle(dir string,p jobs.ScriptParams)bool {
 if p.Validate()!=nil{return false}
 f,err:=os.Open(filepath.Join(dir,p.SHA256+".zip"));if err!=nil{return false};defer f.Close()
 st,err:=f.Stat();if err!=nil||!st.Mode().IsRegular()||st.Size()!=p.Size{return false}
 h:=sha256.New();n,err:=io.Copy(h,io.LimitReader(f,p.Size+1));return err==nil&&n==p.Size&&hex.EncodeToString(h.Sum(nil))==p.SHA256
}
