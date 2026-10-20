package userapp

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// O ícone perto do relógio (área de notificação), pela API do Windows, sem biblioteca de
// fora: uma janela invisível recebe os cliques no ícone; clique esquerdo abre a página,
// clique direito mostra o menu (Abrir, Sair). Os avisos ("há atualização para você") são
// balões do próprio ícone; clicar no balão abre a página.

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	procRegisterClassExW       = user32.NewProc("RegisterClassExW")
	procCreateWindowExW        = user32.NewProc("CreateWindowExW")
	procDefWindowProcW         = user32.NewProc("DefWindowProcW")
	procDestroyWindow          = user32.NewProc("DestroyWindow")
	procGetMessageW            = user32.NewProc("GetMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessageW       = user32.NewProc("DispatchMessageW")
	procPostMessageW           = user32.NewProc("PostMessageW")
	procPostQuitMessage        = user32.NewProc("PostQuitMessage")
	procLoadIconW              = user32.NewProc("LoadIconW")
	procCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	procAppendMenuW            = user32.NewProc("AppendMenuW")
	procTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	procDestroyMenu            = user32.NewProc("DestroyMenu")
	procGetCursorPos           = user32.NewProc("GetCursorPos")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	procShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
)

const (
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmCommand     = 0x0111
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmContextMenu = 0x007B
	wmUser        = 0x0400
	wmApp         = 0x8000

	wmTray   = wmApp + 1 // mensagem dos cliques no ícone
	wmNotice = wmApp + 2 // um aviso novo para mostrar

	ninBalloonUserClick = wmUser + 5

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifInfo    = 0x10
	niifInfo   = 0x1

	idiShield = 32518 // o escudo do Windows: "segurança, atualização"

	mfString    = 0x0
	mfSeparator = 0x800
	tpmRetCmd   = 0x100
	tpmRightBtn = 0x2

	cmdOpen = 1
	cmdQuit = 2

	swShowNormal = 1
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type point struct{ X, Y int32 }

type msg struct {
	HWnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

// notifyIconData é o NOTIFYICONDATAW (versão do Vista em diante), campo a campo.
type notifyIconData struct {
	Size            uint32
	Wnd             windows.HWND
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	TimeoutVersion  uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            windows.GUID
	BalloonIcon     windows.Handle
}

// TrayOptions é o que o ícone precisa.
type TrayOptions struct {
	Tooltip string
	Open    func(tab string) // abre a página ("": a aba padrão)
	Notices <-chan Notice    // avisos para o balão
}

// tray guarda o estado do ícone; a janela é uma só por processo.
type tray struct {
	opts           TrayOptions
	wnd            windows.HWND
	nid            notifyIconData
	taskbarCreated uint32
	mu             sync.Mutex
	pending        *Notice
	balloonTab     string // a aba do último aviso: o clique no balão abre ela
}

var current *tray

// SingleInstance garante um app por sessão (o mutex Local\ é por sessão). ok=false: já
// há um rodando nesta sessão.
func SingleInstance() (release func(), ok bool) {
	name, _ := windows.UTF16PtrFromString(`Local\patchd-central-de-software`)
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		return func() {}, false
	}
	if err != nil {
		return func() {}, true // sem o mutex, segue: no pior caso, dois ícones
	}
	return func() { _ = windows.CloseHandle(h) }, true
}

// RunTray mostra o ícone e roda o laço de mensagens até o contexto acabar ou o usuário
// escolher Sair.
func RunTray(ctx context.Context, opts TrayOptions) error {
	runtime.LockOSThread() // a janela e o laço de mensagens ficam na mesma thread
	defer runtime.UnlockOSThread()

	t := &tray{opts: opts}
	current = t
	var inst windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &inst); err != nil {
		return err
	}
	cls, _ := windows.UTF16PtrFromString("patchdCentralDeSoftware")
	wc := wndClassEx{WndProc: windows.NewCallback(wndProc), Instance: inst, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("RegisterClassEx: %w", err)
	}
	title, _ := windows.UTF16PtrFromString(Title)
	r, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)), 0,
		0, 0, 0, 0, 0, 0, uintptr(inst), 0)
	if r == 0 {
		return fmt.Errorf("CreateWindowEx: %w", err)
	}
	t.wnd = windows.HWND(r)
	tc, _ := windows.UTF16PtrFromString("TaskbarCreated")
	m, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tc)))
	// #nosec G115 -- o Win32 devolve um UINT no uintptr
	t.taskbarCreated = uint32(m)

	icon, _, _ := procLoadIconW.Call(0, idiShield)
	t.nid.Size = uint32(unsafe.Sizeof(t.nid))
	t.nid.Wnd = t.wnd
	t.nid.ID = 1
	t.nid.Flags = nifMessage | nifIcon | nifTip
	t.nid.CallbackMessage = wmTray
	t.nid.Icon = windows.Handle(icon)
	copyUTF16(t.nid.Tip[:], opts.Tooltip)
	if err := t.notify(nimAdd); err != nil {
		return fmt.Errorf("Shell_NotifyIcon: %w", err)
	}

	// Avisos e encerramento chegam de outras goroutines: viram mensagens para a janela.
	go func() {
		for {
			select {
			case <-ctx.Done():
				_, _, _ = procPostMessageW.Call(uintptr(t.wnd), wmClose, 0, 0)
				return
			case n, ok := <-opts.Notices:
				if !ok {
					opts.Notices = nil
					continue
				}
				t.mu.Lock()
				t.pending = &n
				t.mu.Unlock()
				_, _, _ = procPostMessageW.Call(uintptr(t.wnd), wmNotice, 0, 0)
			}
		}
	}()

	var mm msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&mm)), 0, 0, 0)
		// #nosec G115 -- o GetMessage devolve um BOOL (int32) no uintptr
		if int32(r) <= 0 { // 0: WM_QUIT; -1: erro
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&mm)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&mm)))
	}
	return nil
}

func (t *tray) notify(op uintptr) error {
	r, _, err := procShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&t.nid)))
	if r == 0 {
		return err
	}
	return nil
}

func wndProc(hwnd, message, wparam, lparam uintptr) uintptr {
	t := current
	// #nosec G115 -- mensagens do Win32 são UINT
	switch uint32(message) {
	case wmTray:
		// #nosec G115 -- a palavra baixa do lParam, como manda o Shell_NotifyIcon
		switch uint32(lparam) & 0xffff {
		case wmLButtonUp:
			t.opts.Open("")
		case ninBalloonUserClick:
			t.opts.Open(t.balloonTab)
		case wmRButtonUp, wmContextMenu:
			t.menu()
		}
		return 0
	case wmNotice:
		t.balloon()
		return 0
	case wmCommand:
		switch wparam & 0xffff {
		case cmdOpen:
			t.opts.Open("")
		case cmdQuit:
			_, _, _ = procPostMessageW.Call(hwnd, wmClose, 0, 0)
		}
		return 0
	case wmClose:
		t.nid.Flags = 0
		_ = t.notify(nimDelete)
		_, _, _ = procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	// #nosec G115 -- mensagens do Win32 são UINT
	if t != nil && t.taskbarCreated != 0 && uint32(message) == t.taskbarCreated {
		// O Explorer reiniciou: o ícone sumiu junto e é posto de novo.
		t.nid.Flags = nifMessage | nifIcon | nifTip
		_ = t.notify(nimAdd)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, message, wparam, lparam)
	return r
}

func (t *tray) menu() {
	h, _, _ := procCreatePopupMenu.Call()
	if h == 0 {
		return
	}
	defer procDestroyMenu.Call(h)
	open, _ := windows.UTF16PtrFromString("Abrir " + Title) // Aula 20.10: o nome vem do painel
	quit, _ := windows.UTF16PtrFromString("Fechar este ícone")
	_, _, _ = procAppendMenuW.Call(h, mfString, cmdOpen, uintptr(unsafe.Pointer(open)))
	_, _, _ = procAppendMenuW.Call(h, mfSeparator, 0, 0)
	_, _, _ = procAppendMenuW.Call(h, mfString, cmdQuit, uintptr(unsafe.Pointer(quit)))
	var p point
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	// Sem trazer a janela para a frente, o menu não fecha ao clicar fora (documentado).
	_, _, _ = procSetForegroundWindow.Call(uintptr(t.wnd))
	// #nosec G115 -- coordenadas da tela passam por uintptr, como pede o Win32
	cmd, _, _ := procTrackPopupMenu.Call(h, tpmRetCmd|tpmRightBtn, uintptr(p.X), uintptr(p.Y), 0, uintptr(t.wnd), 0)
	if cmd != 0 {
		_, _, _ = procPostMessageW.Call(uintptr(t.wnd), wmCommand, cmd, 0)
	}
}

func (t *tray) balloon() {
	t.mu.Lock()
	n := t.pending
	t.pending = nil
	t.mu.Unlock()
	if n == nil {
		return
	}
	title, text := noticeText(*n)
	if text == "" {
		return
	}
	t.balloonTab = n.Tab()
	t.nid.Flags = nifInfo
	copyUTF16(t.nid.InfoTitle[:], title)
	copyUTF16(t.nid.Info[:], text)
	t.nid.InfoFlags = niifInfo
	_ = t.notify(nimModify)
}

// OpenURL abre o endereço no navegador padrão do usuário.
func OpenURL(u string) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(u)
	_ = windows.ShellExecute(0, verb, file, nil, nil, swShowNormal)
}

// copyUTF16 copia s para o campo de tamanho fixo, cortando e terminando com zero.
func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	clear(dst)
	copy(dst, u)
}

// ErrNoTray: o ícone só existe no Windows.
var ErrNoTray = errors.New("o ícone da área de notificação só existe no Windows")
