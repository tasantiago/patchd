package wincom

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// sFalse é o HRESULT S_FALSE: o COM já estava inicializado nesta thread. Não é erro,
// mas a chamada ainda precisa do CoUninitialize correspondente.
const sFalse = 0x00000001

// Do executa fn numa goroutine presa a uma thread do SO, com o COM inicializado em
// modo multithread (MTA), e desfaz tudo ao final. O COM é inicializado por thread, e o
// escalonador do Go move goroutines entre threads: sem LockOSThread, uma chamada COM
// poderia cair numa thread onde o COM não foi inicializado.
//
// Se ctx terminar antes, Do devolve o erro de ctx, mas a chamada COM em curso continua
// até terminar: uma chamada COM síncrona não pode ser interrompida de fora.
func Do(ctx context.Context, fn func() error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
			var oe *ole.OleError
			if !errors.As(err, &oe) || oe.Code() != sFalse {
				done <- comError("CoInitializeEx", err)
				return
			}
		}
		// Registrado depois do LockOSThread: os defers rodam em ordem inversa, então o
		// COM é desfeito antes de a goroutine soltar a thread.
		defer ole.CoUninitialize()

		done <- fn()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CreateDispatch cria o objeto COM pelo ProgID (o equivalente ao New-Object -ComObject)
// e devolve a interface IDispatch, que chama métodos e lê propriedades pelo nome.
// Deve ser chamada dentro de Do. Quem recebe o objeto chama Release.
func CreateDispatch(progID string) (*ole.IDispatch, error) {
	unknown, err := oleutil.CreateObject(progID)
	if err != nil {
		return nil, comError("criar "+progID, err)
	}
	defer unknown.Release()

	disp, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, comError(progID+": IDispatch", err)
	}
	return disp, nil
}

// Row é um objeto devolvido por uma consulta WMI: nome da propriedade → valor.
type Row map[string]any

// Query executa uma consulta WQL no namespace (ex.: `root\cimv2`) e devolve as
// propriedades pedidas de cada objeto. Deve ser chamada dentro de Do.
func Query(namespace, wql string, props []string) ([]Row, error) {
	locator, err := CreateDispatch("WbemScripting.SWbemLocator")
	if err != nil {
		return nil, err
	}
	defer locator.Release()

	// Servidor nulo = máquina local.
	svc, err := CallObject(locator, "ConnectServer", nil, namespace)
	if err != nil {
		return nil, fmt.Errorf("WMI %s: conectar: %w", namespace, err)
	}
	defer svc.Release()

	res, err := CallObject(svc, "ExecQuery", wql)
	if err != nil {
		return nil, fmt.Errorf("WMI %q: %w", wql, err)
	}
	defer res.Release()

	count, err := GetInt(res, "Count")
	if err != nil {
		return nil, fmt.Errorf("WMI %q: contar resultados: %w", wql, err)
	}

	rows := make([]Row, 0, count)
	for i := 0; i < int(count); i++ {
		row, err := readItem(res, i, props)
		if err != nil {
			return rows, fmt.Errorf("WMI %q: item %d: %w", wql, i, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// readItem lê as propriedades de um objeto do resultado e o libera.
func readItem(res *ole.IDispatch, i int, props []string) (Row, error) {
	item, err := CallObject(res, "ItemIndex", i)
	if err != nil {
		return nil, err
	}
	defer item.Release()

	row := Row{}
	for _, p := range props {
		v, err := Get(item, p)
		if err != nil {
			return nil, err
		}
		row[p] = v
	}
	return row, nil
}

// variantValue converte o VARIANT em valor Go. Arrays (como ChassisTypes) viram []any.
// O valor é copiado antes do Clear do VARIANT.
func variantValue(v *ole.VARIANT) any {
	if v.VT&ole.VT_ARRAY != 0 {
		return v.ToArray().ToValueArray()
	}
	return v.Value()
}

// Param é um parâmetro de entrada de um método WMI (Aula 21.5).
type Param struct {
	Name  string
	Value any
}

// ErrNoInstance: a consulta do ExecMethod não devolveu nenhuma instância.
var ErrNoInstance = errors.New("WMI: nenhuma instância para chamar o método")

// ExecMethod chama um método WMI na primeira instância devolvida por wql (como o
// $obj.Metodo(...) do PowerShell), com os parâmetros de entrada pelo nome, e devolve as
// propriedades pedidas da saída. Deve ser chamada dentro de Do. called diz se o método chegou
// a ser chamado: erro com called = false quer dizer que nada foi executado.
func ExecMethod(namespace, wql, method string, in []Param, out []string) (row Row, called bool, err error) {
	locator, err := CreateDispatch("WbemScripting.SWbemLocator")
	if err != nil {
		return nil, false, err
	}
	defer locator.Release()
	svc, err := CallObject(locator, "ConnectServer", nil, namespace)
	if err != nil {
		return nil, false, fmt.Errorf("WMI %s: conectar: %w", namespace, err)
	}
	defer svc.Release()
	res, err := CallObject(svc, "ExecQuery", wql)
	if err != nil {
		return nil, false, fmt.Errorf("WMI %q: %w", wql, err)
	}
	defer res.Release()
	if n, err := GetInt(res, "Count"); err != nil || n == 0 {
		if err == nil {
			err = ErrNoInstance
		}
		return nil, false, err
	}
	inst, err := CallObject(res, "ItemIndex", 0)
	if err != nil {
		return nil, false, err
	}
	defer inst.Release()
	// Os parâmetros: Methods_(nome).InParameters.SpawnInstance_(), e cada um em Properties_.
	methods, err := GetObject(inst, "Methods_")
	if err != nil {
		return nil, false, err
	}
	defer methods.Release()
	m, err := CallObject(methods, "Item", method)
	if err != nil {
		return nil, false, fmt.Errorf("WMI: método %s: %w", method, err)
	}
	defer m.Release()
	def, err := GetObject(m, "InParameters")
	if err != nil {
		return nil, false, err
	}
	defer def.Release()
	params, err := CallObject(def, "SpawnInstance_")
	if err != nil {
		return nil, false, err
	}
	defer params.Release()
	props, err := GetObject(params, "Properties_")
	if err != nil {
		return nil, false, err
	}
	defer props.Release()
	for _, p := range in {
		item, err := CallObject(props, "Item", p.Name)
		if err != nil {
			return nil, false, fmt.Errorf("WMI: parâmetro %s: %w", p.Name, err)
		}
		err = Put(item, "Value", p.Value)
		item.Release()
		if err != nil {
			// O valor pode ser um segredo: o erro leva só o nome do parâmetro.
			return nil, false, fmt.Errorf("WMI: parâmetro %s: %w", p.Name, err)
		}
	}
	result, err := CallObject(inst, "ExecMethod_", method, params)
	if err != nil {
		return nil, true, fmt.Errorf("WMI: %s: %w", method, err)
	}
	defer result.Release()
	row = Row{}
	for _, name := range out {
		v, err := Get(result, name)
		if err != nil {
			return nil, true, err
		}
		row[name] = v
	}
	return row, true, nil
}
