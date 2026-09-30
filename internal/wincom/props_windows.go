package wincom

import (
	"fmt"
	"time"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// Get lê uma propriedade simples (texto, número, data, booleano, array) e devolve o valor Go.
// Argumentos opcionais servem a propriedades indexadas, como Item(i). Objetos: use GetObject.
func Get(disp *ole.IDispatch, name string, args ...any) (any, error) {
	v, err := oleutil.GetProperty(disp, name, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer v.Clear()
	if v.VT == ole.VT_DISPATCH {
		return nil, fmt.Errorf("%s: é um objeto COM; use GetObject", name)
	}
	return variantValue(v), nil
}

// Call chama um método que devolve um valor simples.
func Call(disp *ole.IDispatch, method string, args ...any) (any, error) {
	v, err := oleutil.CallMethod(disp, method, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	defer v.Clear()
	if v.VT == ole.VT_DISPATCH {
		return nil, fmt.Errorf("%s: devolveu um objeto COM; use CallObject", method)
	}
	return variantValue(v), nil
}

// GetObject lê uma propriedade que é um objeto COM. A referência passa para quem
// recebe: chame Release.
func GetObject(disp *ole.IDispatch, name string, args ...any) (*ole.IDispatch, error) {
	v, err := oleutil.GetProperty(disp, name, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if v.VT != ole.VT_DISPATCH {
		_ = v.Clear()
		return nil, fmt.Errorf("%s: esperado objeto COM, veio tipo %d", name, v.VT)
	}
	return v.ToIDispatch(), nil
}

// CallObject chama um método que devolve um objeto COM. Quem recebe chama Release.
func CallObject(disp *ole.IDispatch, method string, args ...any) (*ole.IDispatch, error) {
	v, err := oleutil.CallMethod(disp, method, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if v.VT != ole.VT_DISPATCH {
		_ = v.Clear()
		return nil, fmt.Errorf("%s: esperado objeto COM, veio tipo %d", method, v.VT)
	}
	return v.ToIDispatch(), nil
}

// Put define uma propriedade.
func Put(disp *ole.IDispatch, name string, value any) error {
	v, err := oleutil.PutProperty(disp, name, value)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	_ = v.Clear()
	return nil
}

// GetString lê uma propriedade de texto; nula vira "".
func GetString(disp *ole.IDispatch, name string, args ...any) (string, error) {
	v, err := Get(disp, name, args...)
	if err != nil || v == nil {
		return "", err
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	return fmt.Sprint(v), nil
}

// GetInt lê uma propriedade numérica inteira; nula vira 0.
func GetInt(disp *ole.IDispatch, name string, args ...any) (int64, error) {
	v, err := Get(disp, name, args...)
	if err != nil {
		return 0, err
	}
	return toInt64(name, v)
}

// CallInt chama um método que devolve um inteiro.
func CallInt(disp *ole.IDispatch, method string, args ...any) (int64, error) {
	v, err := Call(disp, method, args...)
	if err != nil {
		return 0, err
	}
	return toInt64(method, v)
}

// GetBool lê uma propriedade booleana; nula vira false.
func GetBool(disp *ole.IDispatch, name string) (bool, error) {
	v, err := Get(disp, name)
	if err != nil || v == nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%s: esperado booleano, veio %T", name, v)
	}
	return b, nil
}

// GetTime lê uma propriedade de data; nula vira o tempo zero.
func GetTime(disp *ole.IDispatch, name string) (time.Time, error) {
	v, err := Get(disp, name)
	if err != nil || v == nil {
		return time.Time{}, err
	}
	t, ok := v.(time.Time)
	if !ok {
		return time.Time{}, fmt.Errorf("%s: esperado data, veio %T", name, v)
	}
	return t, nil
}

// Strings lê uma coleção de textos (como KBArticleIDs ou CveIDs) exposta na propriedade name.
func Strings(disp *ole.IDispatch, name string) ([]string, error) {
	coll, err := GetObject(disp, name)
	if err != nil {
		return nil, err
	}
	defer coll.Release()

	n, err := GetInt(coll, "Count")
	if err != nil {
		return nil, err
	}
	var out []string
	for i := 0; i < int(n); i++ {
		s, err := GetString(coll, "Item", i)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", name, i, err)
		}
		out = append(out, s)
	}
	return out, nil
}

func toInt64(name string, v any) (int64, error) {
	switch n := v.(type) {
	case nil:
		return 0, nil
	case int8:
		return int64(n), nil
	case int16:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case uint8:
		return int64(n), nil
	case uint16:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("%s: esperado inteiro, veio %T", name, v)
	}
}
