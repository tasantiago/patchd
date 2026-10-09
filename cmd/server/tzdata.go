package main

// A base de fusos horários vai dentro do executável (cerca de 450 KB): a imagem de
// produção é FROM scratch, sem /usr/share/zoneinfo, e as janelas de manutenção (Aula 8.3)
// são calculadas no fuso de cada janela (time.LoadLocation).
import _ "time/tzdata"
