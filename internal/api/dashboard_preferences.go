package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type dashboardOption struct {
	Key, Label string
	Enabled bool
}

var dashboardOptions = []dashboardOption{
	{Key: "indicadores", Label: "Indicadores principais"},
	{Key: "conformidade", Label: "Conformidade da frota"},
	{Key: "seguranca", Label: "Segurança e correções pendentes"},
	{Key: "sistemas", Label: "Composição dos sistemas"},
	{Key: "comunicacao", Label: "Comunicação dos equipamentos"},
	{Key: "execucoes", Label: "Execuções de jobs"},
	{Key: "campanhas", Label: "Situação das campanhas"},
	{Key: "decisoes", Label: "Decisões e acompanhamentos"},
	{Key: "prioridades", Label: "Equipamentos para priorizar"},
}

func (v dashboardView) wants(keys ...string) bool {
	for _, key := range keys {
		if v.Show[key] {
			return true
		}
	}
	return false
}

// Preferências visuais pertencem à conta neste navegador, sem alterar parâmetros globais.
// A lista é fechada; o cookie não controla autorização nem fornece dados ao template.
func (a *api) dashboardPreferences(w http.ResponseWriter, r *http.Request, v *dashboardView) {
	v.Show = map[string]bool{}
	for _, option := range dashboardOptions {
		v.Show[option.Key] = true
	}
	account := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s", v.Session.Source, v.Session.Username)))
	name := fmt.Sprintf("patchd_dashboard_%x", account[:8])
	apply := func(keys []string) {
		for key := range v.Show {
			v.Show[key] = false
		}
		for _, key := range keys {
			if _, known := v.Show[key]; known {
				v.Show[key] = true
			}
		}
	}
	if c, err := r.Cookie(name); err == nil && len(c.Value) <= 256 {
		keys := strings.Split(c.Value, ",")
		valid := c.Value == "none"
		if !valid {
			valid = len(keys) > 0
			for _, key := range keys {
				if _, known := v.Show[key]; !known {
					valid = false
				}
			}
		}
		if valid {
			apply(keys)
		}
	}
	q := r.URL.Query()
	switch q.Get("personalizar") {
	case "1":
		apply(q["bloco"])
		v.Flash = "Sua seleção de gráficos foi salva."
	case "restaurar":
		for key := range v.Show {
			v.Show[key] = true
		}
		v.Flash = "Todos os gráficos foram habilitados."
	}
	var enabled []string
	for _, option := range dashboardOptions {
		option.Enabled = v.Show[option.Key]
		v.Options = append(v.Options, option)
		if option.Enabled {
			enabled = append(enabled, option.Key)
		}
	}
	v.EmptySelection = len(enabled) == 0
	if q.Get("personalizar") == "1" || q.Get("personalizar") == "restaurar" {
		value := strings.Join(enabled, ",")
		if value == "" {
			value = "none"
		}
		http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/painel/", MaxAge: 180*24*60*60,
			Expires: a.now().Add(180*24*time.Hour), HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	}
}
