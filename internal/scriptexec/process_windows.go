package scriptexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/tasantiago/patchd/internal/jobs"
	"golang.org/x/sys/windows"
)

// O processo nasce suspenso: ele só pode criar filhos depois de entrar no Job Object.
// Assim não há janela em que um instalador consiga escapar da árvore controlada.
func runProcess(ctx context.Context, cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("%w: criar Job Object: %v", jobs.ErrNotStarted, err)
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fmt.Errorf("%w: limitar Job Object: %v", jobs.ErrNotStarted, err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if strings.EqualFold(filepath.Base(cmd.Path), "cmd.exe") {
		// Go usa quoting de programas C por padrão; cmd /s /c exige a linha já
		// delimitada por command. Não se permite uma linha arbitrária de shell.
		cmd.SysProcAttr.CmdLine = `"` + cmd.Path + `" ` + strings.Join(cmd.Args[1:], " ")
	}
	cmd.Cancel = func() error {
		if err := windows.TerminateJobObject(job, 1); err != nil {
			return err
		}
		return nil
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrNotStarted, err)
	}
	fail := func(err error) error {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("%w: preparar árvore de processos: %v", jobs.ErrNotStarted, err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := resumeInitialThread(uint32(cmd.Process.Pid)); err != nil {
		return fail(err)
	}
	return cmd.Wait()
}

func resumeInitialThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		return err
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return os.ErrProcessDone
	}
	return err
}
