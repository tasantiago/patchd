//go:build linux || darwin

package userapp

import("os";"path/filepath";"syscall")

func SingleInstance()(func(),bool){
 dir,err:=os.UserConfigDir();if err!=nil{return func(){},false};dir=filepath.Join(dir,"patchd")
 if err:=os.MkdirAll(dir,0700);err!=nil{return func(){},false}
 f,err:=os.OpenFile(filepath.Join(dir,"user-app.lock"),os.O_CREATE|os.O_RDWR,0600);if err!=nil{return func(){},false}
 if err:=syscall.Flock(int(f.Fd()),syscall.LOCK_EX|syscall.LOCK_NB);err!=nil{_ = f.Close();return func(){},false}
 return func(){_ = syscall.Flock(int(f.Fd()),syscall.LOCK_UN);_ = f.Close()},true
}
