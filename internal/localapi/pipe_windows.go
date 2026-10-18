package localapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// pipeSDDL é quem pode abrir o canal (Aula 17.3):
//   - D:P              a lista é só esta (nada herdado);
//   - (D;;GA;;;NU)     LOGON DE REDE: negado (além do PIPE_REJECT_REMOTE_CLIENTS);
//   - (A;;GA;;;SY)     SYSTEM, que é o serviço: tudo;
//   - (A;;GA;;;BA)     Administradores: tudo (o agente rodando num terminal elevado);
//   - (A;;0x12018b;;;IU) INTERATIVO: ler, escrever, atributos e sincronizar. De propósito,
//     sem FILE_CREATE_PIPE_INSTANCE (0x4, o mesmo bit de FILE_APPEND_DATA): um usuário não
//     consegue abrir uma instância do canal e se passar pelo agente para os outros.
//
// 0x12018b = SYNCHRONIZE 0x100000 | READ_CONTROL 0x20000 | FILE_WRITE_ATTRIBUTES 0x100 |
// FILE_READ_ATTRIBUTES 0x80 | FILE_READ_EA 0x8 | FILE_WRITE_DATA 0x2 | FILE_READ_DATA 0x1.
// Sem rótulo de integridade, o padrão é médio: processo de integridade baixa (navegador em
// sandbox) não escreve no canal.
const pipeSDDL = "D:P(D;;GA;;;NU)(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x12018b;;;IU)"

const pipeBuffer = 64 << 10

// maxOpen: conexões abertas ao mesmo tempo (cada uma tem 10 s para mandar o pedido). Acima
// disso, a conexão nova é fechada na hora.
const maxOpen = 32

var (
	advapi32                       = windows.NewLazySystemDLL("advapi32.dll")
	procImpersonateNamedPipeClient = advapi32.NewProc("ImpersonateNamedPipeClient")
)

func impersonateNamedPipeClient(h windows.Handle) error {
	r, _, err := procImpersonateNamedPipeClient.Call(uintptr(h))
	if r == 0 {
		return err
	}
	return nil
}

// Listen atende o canal até o contexto acabar. A primeira instância é criada com
// FILE_FLAG_FIRST_PIPE_INSTANCE: se outro processo já tem um pipe com esse nome (alguém
// tentando se passar pelo agente), o agente não atende e devolve o erro.
func Listen(ctx context.Context, handle func(Conn)) error {
	sd, err := windows.SecurityDescriptorFromString(pipeSDDL)
	if err != nil {
		return fmt.Errorf("descritor de segurança: %w", err)
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	// stop interrompe as esperas de todas as conexões quando o serviço para. Só é fechado
	// depois que ninguém mais o usa: a goroutine que o sinaliza e as conexões.
	var wg sync.WaitGroup
	quit, watcher := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watcher)
		select {
		case <-ctx.Done():
			_ = windows.SetEvent(stop)
		case <-quit:
		}
	}()
	defer func() {
		close(quit)
		<-watcher
		_ = windows.SetEvent(stop)
		wg.Wait()
		windows.CloseHandle(stop)
	}()
	var open atomic.Int32

	name, _ := windows.UTF16PtrFromString(PipeName)
	first := true
	for ctx.Err() == nil {
		flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED)
		if first {
			flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
		}
		h, err := windows.CreateNamedPipe(name, flags,
			windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
			windows.PIPE_UNLIMITED_INSTANCES, pipeBuffer, pipeBuffer, 0, sa)
		if err != nil {
			if first {
				if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
					return fmt.Errorf("outro processo já abriu %s; o agente não vai atender por ele: %w", PipeName, err)
				}
				return fmt.Errorf("criar %s: %w", PipeName, err)
			}
			select { // falha passageira (memória, handles): espera e tenta de novo
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
		first = false
		c, err := newPipeConn(h, stop)
		if err != nil {
			windows.CloseHandle(h)
			return err
		}
		if err := c.connect(); err != nil {
			c.Close()
			continue
		}
		if open.Load() >= maxOpen { // alguém abrindo conexões e não mandando nada
			c.Close()
			continue
		}
		open.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer open.Add(-1)
			handle(c)
		}()
	}
	return nil
}

// pipeConn é a ponta do agente numa conexão. Tudo que o Windows lê ou escreve durante uma
// operação assíncrona (o OVERLAPPED e o buffer) fica nesta struct, no heap: a pilha de uma
// goroutine pode mudar de lugar no meio da operação.
type pipeConn struct {
	h        windows.Handle
	ev       windows.Handle
	stop     windows.Handle
	ov       windows.Overlapped
	buf      [4096]byte
	deadline time.Time
	closed   bool
}

func newPipeConn(h, stop windows.Handle) (*pipeConn, error) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	return &pipeConn{h: h, ev: ev, stop: stop}, nil
}

var errTimeout = errors.New("prazo esgotado")

// wait termina uma operação assíncrona: espera o evento, o prazo ou a parada do agente.
func (c *pipeConn) wait(err error) (uint32, error) {
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return 0, err
	}
	expired := false
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		ms := uint32(windows.INFINITE)
		if !c.deadline.IsZero() {
			d := time.Until(c.deadline)
			if d < 0 {
				d = 0
			}
			ms = uint32(d / time.Millisecond)
		}
		r, werr := windows.WaitForMultipleObjects([]windows.Handle{c.ev, c.stop}, false, ms)
		if werr != nil || r != windows.WAIT_OBJECT_0 {
			expired = true
			_ = windows.CancelIoEx(c.h, &c.ov)
		}
	}
	var n uint32
	err = windows.GetOverlappedResult(c.h, &c.ov, &n, true)
	if expired && errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
		return n, errTimeout
	}
	return n, err
}

func (c *pipeConn) reset() {
	c.ov = windows.Overlapped{HEvent: c.ev}
	_ = windows.ResetEvent(c.ev)
}

func (c *pipeConn) connect() error {
	c.reset()
	err := windows.ConnectNamedPipe(c.h, &c.ov)
	if err == nil || errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return nil
	}
	_, err = c.wait(err)
	return err
}

func (c *pipeConn) Read(p []byte) (int, error) {
	if len(p) > len(c.buf) {
		p = p[:len(c.buf)]
	}
	c.reset()
	var done uint32
	n, err := c.wait(windows.ReadFile(c.h, c.buf[:len(p)], &done, &c.ov))
	switch {
	case errors.Is(err, windows.ERROR_BROKEN_PIPE), errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED):
		return 0, io.EOF
	case err != nil:
		return 0, err
	case n == 0:
		return 0, io.EOF
	}
	return copy(p, c.buf[:n]), nil
}

func (c *pipeConn) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		chunk := copy(c.buf[:], p)
		c.reset()
		var done uint32
		n, err := c.wait(windows.WriteFile(c.h, c.buf[:chunk], &done, &c.ov))
		total += int(n)
		if err != nil {
			return total, err
		}
		p = p[n:]
	}
	return total, nil
}

func (c *pipeConn) SetDeadline(t time.Time) error {
	c.deadline = t
	return nil
}

// Identify lê o token de quem abriu o pipe. O Windows só deixa fazer isso depois de algo
// ter sido lido do pipe. O nível pedido pelo cliente pode ser só "identificação": basta para
// ler o token, e o agente não age como o usuário.
func (c *pipeConn) Identify() (TokenInfo, error) {
	tok, err := c.clientToken()
	if err != nil {
		return TokenInfo{}, err
	}
	defer tok.Close()

	tu, err := tok.GetTokenUser()
	if err != nil {
		return TokenInfo{}, fmt.Errorf("usuário do token: %w", err)
	}
	info := TokenInfo{UserSID: tu.User.Sid.String()}
	info.Account = lookupName(tu.User.Sid)
	tg, err := tok.GetTokenGroups()
	if err != nil {
		return TokenInfo{}, fmt.Errorf("grupos do token: %w", err)
	}
	for _, g := range tg.AllGroups() {
		tg := TokenGroup{SID: g.Sid.String(), Attrs: g.Attributes}
		if KeepGroup(g.Attributes) && tg.SID != sidInteractive {
			tg.Name = lookupName(g.Sid)
		}
		info.Groups = append(info.Groups, tg)
	}
	return info, nil
}

// clientToken personifica o cliente só o bastante para abrir o token da thread e volta
// a ser SYSTEM. A thread fica presa à goroutine nesse meio-tempo; se o RevertToSelf falhar,
// ela continua presa e o Go a descarta quando a goroutine terminar (nunca volta ao pool
// com a identidade do usuário).
func (c *pipeConn) clientToken() (windows.Token, error) {
	runtime.LockOSThread()
	if err := impersonateNamedPipeClient(c.h); err != nil {
		runtime.UnlockOSThread()
		return 0, fmt.Errorf("ImpersonateNamedPipeClient: %w", err)
	}
	var tok windows.Token
	err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &tok)
	if rerr := windows.RevertToSelf(); rerr != nil {
		if err == nil {
			tok.Close()
		}
		return 0, fmt.Errorf("RevertToSelf: %w", rerr)
	}
	runtime.UnlockOSThread()
	if err != nil {
		return 0, fmt.Errorf("OpenThreadToken: %w", err)
	}
	return tok, nil
}

func (c *pipeConn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	_ = windows.DisconnectNamedPipe(c.h)
	windows.CloseHandle(c.ev)
	return windows.CloseHandle(c.h)
}

// Nomes de SID: o grupo do domínio é resolvido pelo controlador (ou pelo cache do LSA), e a
// mesma pessoa abre o app várias vezes. Guardados por uma hora.
var names = struct {
	sync.Mutex
	m map[string]cachedName
}{m: map[string]cachedName{}}

type cachedName struct {
	name string
	at   time.Time
}

const nameTTL = time.Hour

func lookupName(sid *windows.SID) string {
	key := sid.String()
	names.Lock()
	if c, ok := names.m[key]; ok && time.Since(c.at) < nameTTL {
		names.Unlock()
		return c.name
	}
	names.Unlock()
	acc, dom, _, err := sid.LookupAccount("")
	name := ""
	switch {
	case err != nil || acc == "":
	case dom == "":
		name = acc
	default:
		name = dom + `\` + acc
	}
	names.Lock()
	if len(names.m) > 4096 {
		names.m = map[string]cachedName{}
	}
	if name != "" { // falha não fica guardada: o controlador pode voltar
		names.m[key] = cachedName{name, time.Now()}
	}
	names.Unlock()
	return name
}

// dial abre o canal como o usuário. SECURITY_IDENTIFICATION: o agente pode ler quem é o
// usuário, mas não agir como ele. Antes de mandar qualquer coisa, confere que o dono do pipe
// é o SYSTEM (o serviço) ou o grupo Administradores (o agente num terminal elevado): um
// usuário comum não consegue criar um objeto com esses donos.
func dial(ctx context.Context) (*os.File, error) {
	name, _ := windows.UTF16PtrFromString(PipeName)
	for {
		h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.FILE_WRITE_DATA, 0, nil, windows.OPEN_EXISTING,
			windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		switch {
		case err == nil:
			if err := checkOwner(h); err != nil {
				windows.CloseHandle(h)
				return nil, err
			}
			return os.NewFile(uintptr(h), PipeName), nil
		case errors.Is(err, windows.ERROR_FILE_NOT_FOUND):
			return nil, ErrNoAgent
		case errors.Is(err, windows.ERROR_PIPE_BUSY):
			select { // todas as instâncias ocupadas: o agente cria outra logo
			case <-ctx.Done():
				return nil, fmt.Errorf("o agente está ocupado: %w", ctx.Err())
			case <-time.After(200 * time.Millisecond):
			}
		case errors.Is(err, windows.ERROR_ACCESS_DENIED):
			return nil, errors.New("acesso negado ao canal do agente (só usuários com sessão interativa)")
		default:
			return nil, fmt.Errorf("abrir o canal do agente: %w", err)
		}
	}
}

func checkOwner(h windows.Handle) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("dono do canal: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("dono do canal: %w", err)
	}
	if owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return nil
	}
	return fmt.Errorf("o canal %s não é do agente (dono %s); não confie nele e avise o suporte", PipeName, owner.String())
}

// ErrUnsupported: o canal local só existe no Windows.
var ErrUnsupported = errors.New("o autoatendimento local só existe no Windows")
