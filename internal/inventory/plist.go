package inventory

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

// parsePlistDict lê o <dict> de nível mais alto de um plist XML e devolve os valores
// simples (string, integer, real, date, true/false) como texto, indexados pela chave.
// Arrays, dicts aninhados e dados binários são pulados inteiros.
func parsePlistDict(data []byte) (map[string]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	out := map[string]string{}
	inDict := false
	key := ""

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("plist XML inválido: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if !inDict {
				// Tudo antes do primeiro <dict> (como o <plist>) é só invólucro.
				if t.Name.Local == "dict" {
					inDict = true
				}
				continue
			}
			switch t.Name.Local {
			case "key":
				if err := dec.DecodeElement(&key, &t); err != nil {
					return nil, fmt.Errorf("plist XML: chave inválida: %w", err)
				}
			case "string", "integer", "real", "date":
				var v string
				if err := dec.DecodeElement(&v, &t); err != nil {
					return nil, fmt.Errorf("plist XML: valor de %q inválido: %w", key, err)
				}
				if key != "" {
					out[key] = v
				}
				key = ""
			case "true", "false":
				if key != "" {
					out[key] = t.Name.Local
				}
				key = ""
				if err := dec.Skip(); err != nil {
					return nil, err
				}
			default:
				// array, dict aninhado, data: fora do escopo deste leitor.
				key = ""
				if err := dec.Skip(); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if inDict && t.Name.Local == "dict" {
				return out, nil
			}
		}
	}
	if !inDict {
		return nil, errors.New("plist XML sem <dict>")
	}
	return out, nil
}
