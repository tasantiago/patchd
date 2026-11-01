//go:build linux || darwin

package localapi

import (
 "context"
 "errors"
 "os/exec"
 "os/user"
 "runtime"
 "strconv"
 "strings"
 "time"

 "github.com/tasantiago/patchd/internal/protocol"
)

// Os binários distribuídos usam CGO=0: os comandos do sistema consultam NSS/AD,
// ao contrário do fallback de os/user, que pode ler somente /etc/passwd e /etc/group.
func unixIdentity(uid uint32)(TokenInfo,error){
 if uid==0{return TokenInfo{},errors.New("use a sessão do usuário, sem sudo")}
 ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel()
 id:=strconv.FormatUint(uint64(uid),10)
 name,home,err:=unixAccount(ctx,id);if err!=nil{return TokenInfo{},err}
 if home==""||home=="/"||home=="/var/empty"||home=="/nonexistent"{return TokenInfo{},errors.New("conta sem perfil de usuário")}
 if name==""||strings.HasPrefix(name,"-"){return TokenInfo{},errors.New("nome de conta inválido")}
 info:=TokenInfo{UserSID:"unix:"+id,Account:name,Groups:[]TokenGroup{{SID:sidInteractive,Attrs:groupEnabled}}}
 // #nosec G204 -- executável fixo; conta resolvida pelo UID do kernel e sem prefixo de opção, passada como argv sem shell.
 raw,err:=exec.CommandContext(ctx,"/usr/bin/id","-G",name).Output();if err!=nil{return TokenInfo{},errors.New("não foi possível resolver os grupos da sessão")}
 gids:=strings.Fields(string(raw));if len(gids)>protocol.MaxSelfServiceGroups{return TokenInfo{},errors.New("grupos demais")}
 for _,gid:=range gids {if _,err:=strconv.ParseUint(gid,10,32);err!=nil{return TokenInfo{},errors.New("identificador de grupo inválido")}
  group,err:=unixGroup(ctx,gid);if err!=nil{info.Groups=append(info.Groups,TokenGroup{SID:"unix-group:"+gid,Attrs:groupEnabled});continue}
  info.Groups=append(info.Groups,TokenGroup{SID:"unix-group:"+gid,Name:group,Attrs:groupEnabled})
 }
 return info,nil
}

func unixAccount(ctx context.Context,id string)(string,string,error){
 if runtime.GOOS=="linux"{
  // #nosec G204 -- executável e operação fixos; UID/GID numérico validado, passado como argv sem shell.
  b,err:=exec.CommandContext(ctx,"/usr/bin/getent","passwd",id).Output();if err==nil{parts:=strings.Split(strings.TrimSpace(string(b)),":");if len(parts)==7&&parts[2]==id{return parts[0],parts[5],nil}}
 }else{
  // #nosec G204 -- executável e operação fixos; UID/GID numérico validado, passado como argv sem shell.
  b,err:=exec.CommandContext(ctx,"/usr/bin/dscacheutil","-q","user","-a","uid",id).Output();if err==nil{fields:=directoryFields(string(b));if fields["uid"]==id&&fields["name"]!=""{return fields["name"],fields["dir"],nil}}
 }
 u,err:=user.LookupId(id);if err!=nil{return "","",err};return u.Username,u.HomeDir,nil
}

func unixGroup(ctx context.Context,gid string)(string,error){
 if runtime.GOOS=="linux"{
  // #nosec G204 -- executável e operação fixos; UID/GID numérico validado, passado como argv sem shell.
  b,err:=exec.CommandContext(ctx,"/usr/bin/getent","group",gid).Output();if err==nil{p:=strings.Split(strings.TrimSpace(string(b)),":");if len(p)>=3&&p[2]==gid{return p[0],nil}}
 }else{
  // #nosec G204 -- executável e operação fixos; UID/GID numérico validado, passado como argv sem shell.
  b,err:=exec.CommandContext(ctx,"/usr/bin/dscacheutil","-q","group","-a","gid",gid).Output();if err==nil{fields:=directoryFields(string(b));if fields["gid"]==gid&&fields["name"]!=""{return fields["name"],nil}}
 }
 g,err:=user.LookupGroupId(gid);if err!=nil{return "",err};return g.Name,nil
}

func directoryFields(raw string)map[string]string{
 out:=map[string]string{};for _,line:=range strings.Split(raw,"\n"){key,value,ok:=strings.Cut(line,":");if ok{out[strings.TrimSpace(key)]=strings.TrimSpace(value)}};return out
}
