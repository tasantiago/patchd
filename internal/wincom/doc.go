// Package wincom concentra o uso de COM no Windows: inicialização por thread,
// criação de objetos por ProgID e consultas WMI. Todo uso de COM no agente passa
// por Do, que prende a goroutine a uma thread do SO durante o uso.
// Nos demais SOs o pacote existe só com esta documentação.
package wincom
