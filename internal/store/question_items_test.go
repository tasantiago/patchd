package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoItensDaPergunta interface {
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	SetMachineCapabilities(ctx context.Context, id string, caps []string) error
	MachinesWithCapability(ctx context.Context, capability string) ([]string, error)
	CreateQuestion(ctx context.Context, q Question) (int64, error)
	Question(ctx context.Context, id int64) (Question, bool, error)
	NextJobID(ctx context.Context) (int64, error)
	CreateJob(ctx context.Context, j NewJob) error
	SaveQuestionItems(ctx context.Context, jobID int64, machineID string, items []protocol.AnswerItem) error
	QuestionItemGroups(ctx context.Context, id int64, q QuestionItemQuery) ([]QuestionItemGroup, int, error)
	QuestionItemTotals(ctx context.Context, id int64) (QuestionItemTotals, error)
	QuestionItems(ctx context.Context, id int64, key string, limit int) ([]QuestionItem, int, error)
	EachQuestionItem(ctx context.Context, id int64, fn func(QuestionItem) error) error
}

// Os itens de uma pergunta em lista: guardados por máquina (o resultado repetido troca, não
// soma), juntados pela chave com filtro, ordem e páginas, listados por chave e percorridos
// um a um para o CSV.
func testarItensDaPergunta(t *testing.T, st contratoItensDaPergunta) {
	ctx := context.Background()
	for _, id := range []string{"it-a", "it-b", "it-c"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "sha256:" + id, OS: protocol.OSInfo{Hostname: id}}); err != nil {
			t.Fatal(err)
		}
	}
	const cap = "perguntas-usuario-v1"
	for _, id := range []string{"it-c", "it-a"} {
		if err := st.SetMachineCapabilities(ctx, id, []string{"jobs-v1", cap}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := st.MachinesWithCapability(ctx, cap); err != nil || !reflect.DeepEqual(got, []string{"it-a", "it-c"}) {
		t.Fatalf("máquinas com a capacidade: %v %v", got, err)
	}
	// A pergunta de extensões sem filtro tem o argumento vazio.
	qid, err := st.CreateQuestion(ctx, Question{Sensor: "extensoes", Target: "todas as máquinas", Machines: 2, CreatedBy: "ana", Skipped: 1})
	if err != nil {
		t.Fatal(err)
	}
	if q, ok, err := st.Question(ctx, qid); err != nil || !ok || q.Skipped != 1 || q.Arg != "" {
		t.Fatalf("pergunta: %+v %v %v", q, ok, err)
	}
	job := map[string]int64{}
	for _, id := range []string{"it-a", "it-c"} {
		jid, _ := st.NextJobID(ctx)
		if err := st.CreateJob(ctx, NewJob{ID: jid, MachineID: id, Type: "pergunta", Signed: protocol.SignedJob{Payload: "{}", Signature: "s"},
			CreatedBy: "ana", NotAfter: time.Now().Add(time.Hour), QuestionID: qid}); err != nil {
			t.Fatal(err)
		}
		job[id] = jid
	}
	avulso, _ := st.NextJobID(ctx)
	if err := st.CreateJob(ctx, NewJob{ID: avulso, MachineID: "it-a", Type: "rescan", Signed: protocol.SignedJob{Payload: "{}", Signature: "s"},
		CreatedBy: "ana", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	item := func(key, name, version, user, source, note string) protocol.AnswerItem {
		return protocol.AnswerItem{Key: key, Name: name, Version: version, User: user, Source: source, Where: "Default", Note: note}
	}
	a := []protocol.AnswerItem{
		item("bloq", "Bloqueador", "1.10.0", "ana", "Chrome", "acesso a todos os sites"),
		item("bloq", "Bloqueador", "1.9.0", "bruno", "Edge", "acesso a todos os sites"),
		item("solta", "Ladra 100%_x", "0.1", "bruno", "Chrome", "sem compactação (modo de desenvolvedor)"),
	}
	c := []protocol.AnswerItem{
		item("bloq", "Bloqueador (beta)", "1.10.0", "ana", "Chrome", ""),
		item("trad", "Tradutor", "", "Carla", "Firefox", "desativada"),
	}
	// Duas vezes a mesma máquina: o segundo resultado troca o primeiro.
	for _, list := range [][]protocol.AnswerItem{{item("velho", "Velho", "1", "x", "Chrome", "")}, a} {
		if err := st.SaveQuestionItems(ctx, job["it-a"], "it-a", list); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveQuestionItems(ctx, job["it-c"], "it-c", c); err != nil {
		t.Fatal(err)
	}
	// Job de outra máquina, job que não é de pergunta, job que não existe: nada entra.
	for _, bad := range []struct {
		job int64
		id  string
	}{{job["it-a"], "it-c"}, {avulso, "it-a"}, {999999, "it-a"}} {
		if err := st.SaveQuestionItems(ctx, bad.job, bad.id, []protocol.AnswerItem{item("intruso", "Intruso", "1", "x", "Chrome", "")}); err != nil {
			t.Fatal(err)
		}
	}
	if tot, err := st.QuestionItemTotals(ctx, qid); err != nil || tot != (QuestionItemTotals{Items: 5, Keys: 3, Machines: 2, Users: 4}) {
		t.Fatalf("totais: %+v %v", tot, err)
	}

	groups, total, err := st.QuestionItemGroups(ctx, qid, QuestionItemQuery{Limit: 10})
	if err != nil || total != 3 || len(groups) != 3 {
		t.Fatalf("grupos: %+v %d %v", groups, total, err)
	}
	g := groups[0]
	if g.Key != "bloq" || g.Name != "Bloqueador" || g.Machines != 2 || g.Users != 3 || g.Items != 3 || g.Note != "acesso a todos os sites" ||
		!reflect.DeepEqual(g.Versions, []string{"1.9.0", "1.10.0"}) || !reflect.DeepEqual(g.Sources, []string{"Chrome", "Edge"}) {
		t.Errorf("o grupo mais comum: %+v", g)
	}
	if groups[1].Key != "solta" || groups[2].Key != "trad" || len(groups[2].Versions) != 0 {
		t.Errorf("empate em máquinas: pelo nome: %+v", groups[1:])
	}
	// As raras primeiro; pelo nome; páginas.
	if groups, _, _ = st.QuestionItemGroups(ctx, qid, QuestionItemQuery{Sort: ItemSortRare, Limit: 10}); groups[2].Key != "bloq" || groups[0].Key != "solta" {
		t.Errorf("raras: %+v", groups)
	}
	if groups, _, _ = st.QuestionItemGroups(ctx, qid, QuestionItemQuery{Sort: ItemSortName, Limit: 10}); groups[0].Key != "bloq" || groups[1].Key != "solta" || groups[2].Key != "trad" {
		t.Errorf("pelo nome: %+v", groups)
	}
	if groups, total, _ = st.QuestionItemGroups(ctx, qid, QuestionItemQuery{Limit: 1, Offset: 1}); total != 3 || len(groups) != 1 || groups[0].Key != "solta" {
		t.Errorf("segunda página: %+v %d", groups, total)
	}
	// O filtro: pelo usuário (sem diferença de maiúsculas) conta só o que é dele; pela
	// observação; e "%" e "_" são texto, não curingas.
	if groups, total, _ = st.QuestionItemGroups(ctx, qid, QuestionItemQuery{Text: "carla", Limit: 10}); total != 1 || groups[0].Key != "trad" {
		t.Errorf("filtro pelo usuário: %+v", groups)
	}
	if groups, _, _ = st.QuestionItemGroups(ctx, qid, QuestionItemQuery{Text: "BRUNO", Limit: 10}); len(groups) != 2 || groups[0].Machines != 1 || groups[0].Users != 1 {
		t.Errorf("filtro conta só os itens que casam: %+v", groups)
	}
	if groups, _, _ = st.QuestionItemGroups(ctx, qid, QuestionItemQuery{Text: "desenvolvedor", Limit: 10}); len(groups) != 1 || groups[0].Key != "solta" {
		t.Errorf("filtro pela observação: %+v", groups)
	}
	if groups, _, _ = st.QuestionItemGroups(ctx, qid, QuestionItemQuery{Text: "100%_x", Limit: 10}); len(groups) != 1 || groups[0].Key != "solta" {
		t.Errorf("curingas como texto: %+v", groups)
	}
	if groups, total, _ = st.QuestionItemGroups(ctx, qid, QuestionItemQuery{Text: "%", Limit: 10}); total != 1 || len(groups) != 1 {
		t.Errorf("%% sozinho só casa com quem tem %%: %+v", groups)
	}
	if groups, total, _ = st.QuestionItemGroups(ctx, qid, QuestionItemQuery{Text: "nada disso", Limit: 10}); total != 0 || len(groups) != 0 {
		t.Errorf("filtro sem achado: %+v", groups)
	}

	// Quem tem a chave, em ordem de máquina e usuário; o limite corta, o total não.
	list, n, err := st.QuestionItems(ctx, qid, "bloq", 2)
	if err != nil || n != 3 || len(list) != 2 || list[0].MachineID != "it-a" || list[0].User != "ana" || list[1].User != "bruno" || list[1].Source != "Edge" {
		t.Errorf("itens da chave: %+v %d %v", list, n, err)
	}
	if list, n, _ = st.QuestionItems(ctx, qid, "nao-existe", 10); n != 0 || len(list) != 0 {
		t.Errorf("chave que não existe: %+v", list)
	}
	var order []string
	if err := st.EachQuestionItem(ctx, qid, func(it QuestionItem) error {
		order = append(order, it.MachineID+"/"+it.User+"/"+it.Key)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"it-a/ana/bloq", "it-a/bruno/solta", "it-a/bruno/bloq", "it-c/ana/bloq", "it-c/Carla/trad"}; !reflect.DeepEqual(order, want) {
		t.Errorf("percurso para o CSV:\n%v\n%v", order, want)
	}
}

func TestItensDaPerguntaMemoria(t *testing.T) { testarItensDaPergunta(t, NewMemory()) }

func TestItensDaPerguntaPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarItensDaPergunta(t, NewPostgres(pool))
	// Apagar a pergunta leva os itens.
	var n int
	if _, err := pool.Exec(context.Background(), `DELETE FROM questions`); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM question_items`).Scan(&n); err != nil || n != 0 {
		t.Errorf("itens órfãos: %d %v", n, err)
	}
}

func TestOrdemDasVersoes(t *testing.T) {
	v := []string{"1.10.0", "1.9.0", "1.9", "2.0b", "2.0a", "10.0", "1.9.0.1"}
	sortVersions(v)
	if want := []string{"1.9", "1.9.0", "1.9.0.1", "1.10.0", "2.0a", "2.0b", "10.0"}; !reflect.DeepEqual(v, want) {
		t.Errorf("%v", v)
	}
}
