package bioswrite

import (
	"context"
	"fmt"

	"github.com/tasantiago/patchd/internal/biosconfig"
	"github.com/tasantiago/patchd/internal/biosvendor"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wincom"
)

// Dell e Lenovo (dell.go e lenovo.go): as chamadas.

type dellVendor struct{}

func (dellVendor) Name() string                            { return "dell" }
func (dellVendor) SureAdmin(context.Context) (bool, error) { return false, nil }

func (dellVendor) SetAdminPassword(ctx context.Context, newPW, current []byte) (Reply, bool, error) {
	st, n, h := dellSecurity(current)
	defer clear(h)
	return dellCall(ctx, biosvendor.DellSecurityNamespace, "SELECT * FROM SecurityInterface", "SetNewPassword", []wincom.Param{
		{Name: "SecType", Value: st}, {Name: "SecHndCount", Value: n}, {Name: "SecHandle", Value: h},
		{Name: "NameId", Value: "Admin"}, {Name: "OldPassword", Value: string(current)}, {Name: "NewPassword", Value: string(newPW)}})
}

func (dellVendor) Settings(ctx context.Context) ([]protocol.BIOSSetting, error) {
	var out []protocol.BIOSSetting
	err := wincom.Do(ctx, func() error {
		var err error
		out, err = biosvendor.ReadDell()
		return err
	})
	return out, err
}

func (dellVendor) SetSetting(ctx context.Context, name, value string, password []byte) (Reply, bool, error) {
	st, n, h := dellSecurity(password)
	defer clear(h)
	return dellCall(ctx, biosvendor.DellAttrNamespace, "SELECT * FROM BIOSAttributeInterface", "SetAttribute", []wincom.Param{
		{Name: "SecType", Value: st}, {Name: "SecHndCount", Value: n}, {Name: "SecHandle", Value: h},
		{Name: "AttributeName", Value: name}, {Name: "AttributeValue", Value: value}})
}

// Commit: a Dell aplica cada SetAttribute sozinho.
func (dellVendor) Commit(context.Context, []byte) (Reply, bool, error) {
	return Reply{Code: "-", OK: true}, false, nil
}

func dellCall(ctx context.Context, ns, wql, method string, in []wincom.Param) (Reply, bool, error) {
	var (
		reply  Reply
		called bool
	)
	err := wincom.Do(ctx, func() error {
		row, c, err := wincom.ExecMethod(ns, wql, method, in, []string{"Status"})
		called = c
		if err != nil {
			return fmt.Errorf("interface da Dell: %w", err)
		}
		code, err := asCode(row["Status"])
		reply = dellReply(code)
		return err
	})
	return reply, called, err
}

type lenovoVendor struct{}

func (lenovoVendor) Name() string                            { return "lenovo" }
func (lenovoVendor) SureAdmin(context.Context) (bool, error) { return false, nil }

func (lenovoVendor) SetAdminPassword(ctx context.Context, newPW, current []byte) (Reply, bool, error) {
	line, err := lenovoPasswordLine(newPW, current)
	if err != nil {
		return Reply{}, false, err
	}
	return lenovoCall(ctx, "Lenovo_SetBiosPassword", "SetBiosPassword", line)
}

func (lenovoVendor) Settings(ctx context.Context) ([]protocol.BIOSSetting, error) {
	var out []protocol.BIOSSetting
	err := wincom.Do(ctx, func() error {
		var err error
		out, err = biosvendor.ReadLenovo(func(name string) bool { _, ok := biosconfig.Lookup(name); return ok })
		return err
	})
	return out, err
}

func (lenovoVendor) SetSetting(ctx context.Context, name, value string, password []byte) (Reply, bool, error) {
	line, err := lenovoSettingLine(name, value, password)
	if err != nil {
		return Reply{}, false, err
	}
	return lenovoCall(ctx, "Lenovo_SetBiosSetting", "SetBiosSetting", line)
}

func (lenovoVendor) Commit(ctx context.Context, password []byte) (Reply, bool, error) {
	line, err := lenovoSaveLine(password)
	if err != nil {
		return Reply{}, false, err
	}
	return lenovoCall(ctx, "Lenovo_SaveBiosSettings", "SaveBiosSettings", line)
}

// lenovoCall chama o método com a linha (vazia: sem parâmetro). A linha pode ter a senha: o
// erro nunca a leva.
func lenovoCall(ctx context.Context, class, method, line string) (Reply, bool, error) {
	var (
		reply  Reply
		called bool
	)
	var in []wincom.Param
	if line != "" {
		in = []wincom.Param{{Name: "parameter", Value: line}}
	}
	err := wincom.Do(ctx, func() error {
		row, c, err := wincom.ExecMethod(biosvendor.LenovoNamespace, "SELECT * FROM "+class, method, in, []string{"return"})
		called = c
		if err != nil {
			return fmt.Errorf("interface da Lenovo: %w", err)
		}
		ret, _ := row["return"].(string)
		reply = lenovoReply(ret)
		return nil
	})
	return reply, called, err
}
