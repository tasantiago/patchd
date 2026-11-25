package userapp

import (
	"html/template"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Atualizações do Windows pela Central: o que a TI já aprovou e a busca deste computador mostra
// pendente. O botão só pede; quem decide o que instalar é o servidor, na hora. O computador
// não reinicia sozinho: o reinício continua sendo a aba Reinício.

type winView struct {
	Updates []protocol.SelfServiceWinUpdate
	Waiting int
	JobID   int64
	Problem string
	ScanAt  string
	Restart bool // alguma vai pedir reinício
	Exploit int  // quantas corrigem falha com exploração conhecida
}

func (a *App) windowsView(st *protocol.SelfServiceWindowsState) *winView {
	if st == nil {
		return nil
	}
	v := &winView{Updates: st.Updates, Waiting: st.Waiting, JobID: st.JobID, Problem: st.Problem}
	if st.ScanAt != nil {
		v.ScanAt = st.ScanAt.In(a.loc()).Format("02/01 15:04")
	}
	for _, u := range st.Updates {
		v.Restart = v.Restart || u.Restart
		if u.Exploited {
			v.Exploit++
		}
	}
	return v
}

var _ = template.Must(pageTmpl.New("windows").Parse(`{{with .Windows}}<section class="windows"><div class="cab"><h2>Windows</h2>
{{if and .Updates (not .JobID) (not .Problem)}}<form method="post" action="{{$.Base}}pedir"><input type="hidden" name="action" value="windows"><button type="submit">Instalar atualizações do Windows ({{len .Updates}})</button></form>{{end}}</div>
{{if .JobID}}<p class="aviso andamento" role="status"><span class="gira" aria-hidden="true"></span>As atualizações do Windows estão sendo instaladas neste computador (pedido nº {{.JobID}}). Pode demorar; você pode continuar trabalhando. Esta página se atualiza sozinha.</p>
{{else if .Problem}}<p class="vazio">A última busca de atualizações deste computador falhou; por enquanto, só a equipe de TI instala as do Windows aqui.</p>
{{else if .Updates}}<ul class="lista">{{range .Updates}}<li><div><strong>{{if .Title}}{{.Title}}{{else}}Atualização KB{{.KB}}{{end}}</strong><span class="ver">KB{{.KB}}{{if .Restart}} · pede reinício{{end}}</span></div>{{if .Exploited}}<span class="selo atencao">corrige falha já explorada</span>{{else}}<span class="selo neutro">aprovada pela TI</span>{{end}}</li>{{end}}</ul>
<p class="nota">A instalação roda em segundo plano e pode levar vários minutos. O computador não reinicia sozinho{{if .Restart}}: quando terminar, use a aba Reinício para escolher a hora{{end}}.{{if .Exploit}} {{.Exploit}} delas corrige(m) falha com exploração conhecida: instale assim que puder.{{end}}</p>
{{else if .Waiting}}<p class="vazio">{{.Waiting}} atualização(ões) do Windows aguardam a liberação da equipe de TI. Quando forem aprovadas, aparecem aqui.</p>
{{else}}<p class="vazio">✓ O Windows está em dia{{if .ScanAt}} (busca de {{.ScanAt}}){{end}}.</p>{{end}}
</section>{{end}}`))
