package standardization

import (
 "context"
 "crypto/ed25519"
 "crypto/rand"
 "crypto/sha256"
 "encoding/hex"
 "os"
 "path/filepath"
 "slices"
 "strings"
 "sync"
 "testing"
 "time"

 "github.com/tasantiago/patchd/internal/jobs"
 "github.com/tasantiago/patchd/internal/protocol"
 "github.com/tasantiago/patchd/internal/store"
 "github.com/tasantiago/patchd/internal/thirdparty"
)

func TestResolveGroupsAndConflicts(t *testing.T){
 link:=protocol.StandardShortcut{Name:"Portal",Kind:"site",Target:"https://example.com"}
 policies:=[]store.StandardizationPolicy{
  {ID:1,Name:"Básico",Enabled:true,AllMachines:true,Apps:[]string{"7zip"},Shortcuts:[]protocol.StandardShortcut{link}},
  {ID:2,Name:"Financeiro",Enabled:true,Machines:[]string{"pc"},Apps:[]string{"7zip","vscode"}},
  {ID:3,Name:"RH",Enabled:true,ADGroups:[]string{`CORP\RH`},Shortcuts:[]protocol.StandardShortcut{{Name:"Portal",Kind:"site",Target:"https://rh.example"}}},
  {ID:4,Name:"Inativo",AllMachines:true,Apps:[]string{"ignorado"}},
 }
 p,ids:=Resolve(policies,"pc",protocol.SelfServiceUser{Account:`CORP\ana`,Groups:[]string{`corp\rh`}},"windows")
 if !slices.Equal(ids,[]string{"7zip","vscode"})||len(p.Policies)!=3||len(p.Shortcuts)!=0||len(p.Warnings)!=1{t.Fatalf("união/conflito: %+v %v",p,ids)}
 p2,_:=Resolve(policies,"outro",protocol.SelfServiceUser{Account:`CORP\ana`,Groups:[]string{`OUTRO\RH`}},"linux")
 if len(p2.Policies)!=1||len(p2.Shortcuts)!=1||p.Revision==p2.Revision{t.Fatalf("filtro de domínio: %+v",p2)}
 if !GroupMember("Financeiro",[]string{`CORP\Financeiro`})||GroupMember(`CORP\RH`,[]string{`OUTRO\RH`}){t.Fatal("associação incorreta")}
 policies[2].Shortcuts[0]=link;p,_=Resolve(policies,"pc",protocol.SelfServiceUser{Groups:[]string{`CORP\RH`}},"windows");if len(p.Shortcuts)!=1||len(p.Warnings)!=0{t.Fatalf("atalho idêntico não deduplicado: %+v",p)}
}

func scriptFixture(t *testing.T,platform string)(*Service,*store.Memory,store.StandardizationPolicy,protocol.InventoryReport){
 t.Helper();st:=store.NewMemory();_,key,err:=ed25519.GenerateKey(rand.Reader);if err!=nil{t.Fatal(err)}
 data:=[]byte("conteúdo do instalador de teste");sum:=sha256.Sum256(data);sha:=hex.EncodeToString(sum[:]);dir:=t.TempDir();if err:=os.WriteFile(filepath.Join(dir,sha+".zip"),data,0600);err!=nil{t.Fatal(err)}
 entry,runner:="install.sh","bash";if platform=="windows"{entry,runner="install.ps1","powershell"}
 params:=jobs.ScriptParams{Bundle:"app-teste",SHA256:sha,Size:int64(len(data)),Entry:entry,Runner:runner,Timeout:600}
 policy:=store.StandardizationPolicy{Name:"Financeiro",Enabled:true,AllMachines:true,Installers:[]store.StandardInstaller{{BundleID:1,OS:platform,Software:"App Financeiro",Params:params}}}
 if _,err:=st.SaveStandardizationPolicy(context.Background(),policy);err!=nil{t.Fatal(err)}
 inv:=protocol.InventoryReport{Hash:"initial",OS:protocol.OSInfo{Family:platform,ID:platform,Arch:"amd64"},Software:[]protocol.Software{}}
 if _,err:=st.SaveInventory(context.Background(),"pc",inv);err!=nil{t.Fatal(err)}
 now:=time.Now().UTC();return &Service{Store:st,Key:key,BundlesDir:dir,Now:func()time.Time{return now}},st,policy,inv
}

func TestAutomaticInstallSignedIdempotentAndInventoryConfirmed(t *testing.T){
 for _,platform:=range []string{"windows","linux","darwin"}{t.Run(platform,func(t *testing.T){
  s,st,_,inv:=scriptFixture(t,platform);ctx:=context.Background();var wg sync.WaitGroup;errs:=make(chan error,8)
  for range 8{wg.Add(1);go func(){defer wg.Done();_,err:=s.Reconcile(ctx,"pc",protocol.SelfServiceUser{});errs<-err}()};wg.Wait();close(errs);for err:=range errs{if err!=nil{t.Fatal(err)}}
  list,err:=st.Jobs(ctx,"pc",20);if err!=nil||len(list)!=1{t.Fatalf("instalação duplicada: %v %v",list,err)}
  envelopes,err:=st.DeliverJobs(ctx,"pc",s.Now());if err!=nil||len(envelopes)!=1{t.Fatalf("entrega: %v %v",envelopes,err)}
  signed,err:=jobs.Verify(s.Key.Public().(ed25519.PublicKey),envelopes[0],"pc",s.Now(),0);if err!=nil||signed.Type!=jobs.TypeScript{t.Fatalf("assinatura: %+v %v",signed,err)}
  if _,err:=st.FinishJob(ctx,signed.ID,jobs.StateDone,"instalado",s.Now());err!=nil{t.Fatal(err)}
  p,err:=s.Reconcile(ctx,"pc",protocol.SelfServiceUser{});if err!=nil||p.Apps[0].State=="em_dia"||p.CreatedJobs!=0{t.Fatalf("conclusão sem inventário não é conformidade: %+v %v",p,err)}
  inv.Hash="installed";inv.Software=[]protocol.Software{{Name:"App Financeiro",Scope:"machine",Version:"1"}};if _,err:=st.SaveInventory(ctx,"pc",inv);err!=nil{t.Fatal(err)}
  p,err=s.Reconcile(ctx,"pc",protocol.SelfServiceUser{});if err!=nil||p.CreatedJobs!=0||p.Apps[0].State!="em_dia"{t.Fatalf("confirmar: %+v %v",p,err)}
 })}
}

func TestMissingOrChangedInstallerAndMissingInventoryBlocked(t *testing.T){
 for _,kind:=range []string{"cache","key","inventory"}{t.Run(kind,func(t *testing.T){
  s,st,p,inv:=scriptFixture(t,"linux");switch kind{case "cache":if err:=os.WriteFile(filepath.Join(s.BundlesDir,p.Installers[0].Params.SHA256+".zip"),[]byte("alterado"),0600);err!=nil{t.Fatal(err)};case "key":s.Key=nil;case "inventory":inv.Hash="failed";inv.Software=nil;if _,err:=st.SaveInventory(context.Background(),"pc",inv);err!=nil{t.Fatal(err)}}
  plan,err:=s.Reconcile(context.Background(),"pc",protocol.SelfServiceUser{});if err!=nil||plan.CreatedJobs!=0||len(plan.Apps)!=1||plan.Apps[0].State!="bloqueado"{t.Fatalf("bloqueio: %+v %v",plan,err)}
 })}
}

func TestWindowsCatalogRequiresApprovalAndPreservesInstallerKind(t *testing.T){
 s,st,_,inv:=scriptFixture(t,"windows");ctx:=context.Background();policies,_:=st.StandardizationPolicies(ctx);p:=policies[0];p.Installers=nil;p.Apps=[]string{"7zip"};if _,err:=st.SaveStandardizationPolicy(ctx,p);err!=nil{t.Fatal(err)}
 manifest,err:=thirdparty.Default();if err!=nil{t.Fatal(err)};s.Manifest=&manifest;s.PackagesDir=t.TempDir()
 data:=[]byte("instalador exe de teste");sum:=sha256.Sum256(data);sha:=hex.EncodeToString(sum[:]);if err:=os.WriteFile(filepath.Join(s.PackagesDir,sha+".exe"),data,0600);err!=nil{t.Fatal(err)}
 pkg:=store.ThirdPartyPackage{AppID:"7zip",WingetID:"7zip.7zip",Version:"26.04",Architecture:"x64",Type:"exe",Arguments:"/S",SHA256:sha,Size:int64(len(data))}
 if _,err:=st.SaveThirdPartyPackage(ctx,pkg);err!=nil{t.Fatal(err)}
 plan,err:=s.Reconcile(ctx,"pc",protocol.SelfServiceUser{});if err!=nil||plan.CreatedJobs!=0{t.Fatalf("instalou sem aprovação: %+v %v",plan,err)}
 if _,err:=st.ApproveThirdParty(ctx,store.ThirdPartyApproval{AppID:"7zip",Version:"26.04",ApprovedBy:"admin"});err!=nil{t.Fatal(err)}
 plan,err=s.Reconcile(ctx,"pc",protocol.SelfServiceUser{});if err!=nil||plan.CreatedJobs!=1{t.Fatalf("não instalou aprovado: %+v %v",plan,err)}
 inv.Hash="installed";inv.Software=[]protocol.Software{{Name:"7-Zip 26.04 (x64)",Version:"26.04",Publisher:"Igor Pavlov",Scope:"machine"}};if _,err:=st.SaveInventory(ctx,"pc",inv);err!=nil{t.Fatal(err)}
 plan,err=s.Reconcile(ctx,"pc",protocol.SelfServiceUser{});if err!=nil||plan.Apps[0].State!="em_dia"||!strings.Contains(plan.Apps[0].Detail,"inventário"){t.Fatalf("confirmação: %+v %v",plan,err)}
}
