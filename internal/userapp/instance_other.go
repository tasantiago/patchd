//go:build !windows && !linux && !darwin

package userapp

func SingleInstance()(func(),bool){return func(){},true}
