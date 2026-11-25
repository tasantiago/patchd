package shield

import "testing"

func maq(b, sala, faixa string, selo bool, score float64) PlaceScore {
	return PlaceScore{Place: Place{Building: b, Room: sala, Floor: faixa}, Sealed: selo, Score: &score}
}

func achar(rs []Rank, kind, b, name string) *Rank {
	for i := range rs {
		if rs[i].Kind == kind && rs[i].Building == b && rs[i].Name == name {
			return &rs[i]
		}
	}
	return nil
}

func TestRankingsPorLugar(t *testing.T) {
	p := Default()
	p.Ranking.MinGroup, p.Ranking.MinRoom = 2, 2
	var l []PlaceScore
	// Sede: duas faixas (dois andares), salas 101 e 201.
	l = append(l, maq("Sede", "101", "f1", true, 100), maq("Sede", "101", "f1", true, 100), maq("Sede", "201", "f2", false, 80), maq("Sede", "201", "f2", true, 100))
	// Anexo: uma faixa só (sem ranking de andar); sala 1 com uma máquina só (pequena demais).
	l = append(l, maq("Anexo", "1", "f3", false, 50), maq("Anexo", "2", "f3", false, 60), maq("Anexo", "2", "f3", true, 100))
	// Sem prédio (fora do patrimônio): fora de tudo. Sem dado: conta como zero.
	l = append(l, maq("", "", "f1", true, 100), PlaceScore{Place: Place{Building: "Anexo", Room: "2", Floor: "f3"}})
	rs := PlaceRankings(&p, l)
	sede, anexo := achar(rs, RankBuilding, "Sede", ""), achar(rs, RankBuilding, "Anexo", "")
	if sede == nil || anexo == nil || sede.Pos != 1 || anexo.Pos != 2 || sede.Of != 2 {
		t.Fatalf("prédios: %+v %+v", sede, anexo)
	}
	if sede.Machines != 4 || sede.Sealed != 3 || sede.Percent() != 75 || sede.Average != 95 {
		t.Fatalf("sede: %+v", sede)
	}
	if anexo.Machines != 4 || anexo.Average != 52.5 {
		t.Fatalf("o sem dado conta como zero no anexo: %+v", anexo)
	}
	f1, f2 := achar(rs, RankFloor, "Sede", "f1"), achar(rs, RankFloor, "Sede", "f2")
	if f1 == nil || f2 == nil || f1.Pos != 1 || f2.Pos != 2 || f1.Of != 2 {
		t.Fatalf("andares da sede: %+v %+v", f1, f2)
	}
	if achar(rs, RankFloor, "Anexo", "f3") != nil {
		t.Fatal("prédio de uma faixa só ganhou ranking de andar")
	}
	if achar(rs, RankRoom, "Anexo", "1") != nil {
		t.Fatal("sala com uma máquina apareceu")
	}
	s2 := achar(rs, RankRoom, "Anexo", "2")
	if s2 == nil || s2.Pos != 1 || s2.Of != 1 || s2.Overall != 3 {
		t.Fatalf("sala 2 do anexo: %+v", s2)
	}
	if s := achar(rs, RankRoom, "Sede", "101"); s == nil || s.Overall != 1 {
		t.Fatalf("sala 101 é a melhor de todas: %+v", s)
	}
	// Desligados: nada daquele tipo.
	p.Ranking.Rooms, p.Ranking.Floors = false, false
	for _, r := range PlaceRankings(&p, l) {
		if r.Kind != RankBuilding {
			t.Fatalf("ranking desligado apareceu: %+v", r)
		}
	}
	// Política antiga (sem os campos novos): ligados por padrão.
	old, err := Parse([]byte(`{"enabled":true,"ranking":{"collective":true,"individual":false,"min_group":5}}`))
	if err != nil || !old.Ranking.Buildings || !old.Ranking.Floors || !old.Ranking.Rooms || old.Ranking.MinRoom != 3 {
		t.Fatalf("%+v %v", old.Ranking, err)
	}
	renomeada, err := Parse([]byte(`{"brand":{"name":"PatchD Shield","mission":"Missão 100%","slogan":"x"}}`))
	if err != nil || renomeada.Brand.Name != "Atende+ Shield" || renomeada.Brand.Slogan != "x" {
		t.Fatalf("nome padrão antigo: %+v %v", renomeada.Brand, err)
	}
	if outro, _ := Parse([]byte(`{"brand":{"name":"Meu Escudo","mission":"M","slogan":"x"}}`)); outro.Brand.Name != "Meu Escudo" {
		t.Fatalf("nome do admin trocado: %+v", outro.Brand)
	}
	bad := Default()
	bad.Ranking.FloorNames = map[string]string{"10.1.2.0/24": ""}
	if bad.Validate() == nil {
		t.Fatal("andar sem nome aceito")
	}
}
