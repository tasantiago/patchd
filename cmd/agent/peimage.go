package main

import (
	"encoding/binary"
	"errors"
)

// guiImage devolve o executável do Windows marcado como programa gráfico (Aula 17.4). O Go
// gera programas de console: aberto pela chave Run, o app mostraria uma janela preta. O
// subsistema é um campo de 2 bytes no cabeçalho opcional do PE (offset 68, igual no PE32 e no
// PE32+): 3 = console, 2 = gráfico. O resto do arquivo fica igual (os binários do patchd não
// têm assinatura Authenticode, que esta troca invalidaria; a garantia deles é a do manifesto
// assinado da release, conferida antes da instalação).
func guiImage(b []byte) ([]byte, error) {
	if len(b) < 0x40 || b[0] != 'M' || b[1] != 'Z' {
		return nil, errors.New("não é um executável do Windows (sem MZ)")
	}
	pe := int(binary.LittleEndian.Uint32(b[0x3c:]))
	const subsystem = 24 + 68 // assinatura (4) + cabeçalho COFF (20) + offset no opcional
	if pe < 0 || pe+subsystem+2 > len(b) || string(b[pe:pe+4]) != "PE\x00\x00" {
		return nil, errors.New("não é um executável do Windows (sem PE)")
	}
	if magic := binary.LittleEndian.Uint16(b[pe+24:]); magic != 0x10b && magic != 0x20b {
		return nil, errors.New("cabeçalho opcional desconhecido")
	}
	out := append([]byte(nil), b...)
	binary.LittleEndian.PutUint16(out[pe+subsystem:], 2) // IMAGE_SUBSYSTEM_WINDOWS_GUI
	return out, nil
}
