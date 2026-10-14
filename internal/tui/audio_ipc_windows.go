//go:build windows

package tui

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func dialAudioIPC(ctx context.Context, endpoint string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, endpoint)
}

// Windows 10+: job membership is atomic with process creation. A separate
// Start + AssignProcessToJobObject (even suspended) leaves an orphan window if
// the parent is killed between those calls. The job is never inherited by mpv;
// Windows closes its last handle on any parent exit and kills the entire tree.
func startAudioProcess(ctx context.Context, player string, args []string) (*audioProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(player) {
		return nil, fmt.Errorf("mpv executable path must be absolute")
	}
	application, err := windows.UTF16PtrFromString(player)
	if err != nil {
		return nil, err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{player}, args...)))
	if err != nil {
		return nil, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create audio job: %w", err)
	}
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { _ = windows.CloseHandle(job) }) }
	fail := func(err error) (*audioProcess, error) { stop(); return nil, err }
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		return fail(fmt.Errorf("configure audio job: %w", err))
	}

	// Inherit only one NUL handle for stdio, never the job, UI input, or unrelated
	// handles. No Go pipe-copy workers are needed, so descendants cannot hang Wait.
	nullName, _ := windows.UTF16PtrFromString("NUL")
	security := windows.SecurityAttributes{InheritHandle: 1}
	security.Length = uint32(unsafe.Sizeof(security))
	null, err := windows.CreateFile(nullName, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &security, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(null)
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return fail(err)
	}
	defer attributes.Delete()
	const jobList = 0x0002000d // PROC_THREAD_ATTRIBUTE_JOB_LIST (Win10+).
	if err = attributes.Update(jobList, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		return fail(fmt.Errorf("set audio job list: %w", err))
	}
	if err = attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&null), unsafe.Sizeof(null)); err != nil {
		return fail(err)
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	startup.ShowWindow = windows.SW_HIDE
	startup.StdInput, startup.StdOutput, startup.StdErr = null, null, null
	var info windows.ProcessInformation
	flags := uint32(windows.CREATE_NO_WINDOW | windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if err = windows.CreateProcess(application, command, nil, nil, true, flags, nil, nil, &startup.StartupInfo, &info); err != nil {
		return fail(err)
	}
	_ = windows.CloseHandle(info.Thread)
	var waitOnce sync.Once
	var waitErr error
	wait := func() error {
		waitOnce.Do(func() {
			defer windows.CloseHandle(info.Process)
			_, waitErr = windows.WaitForSingleObject(info.Process, windows.INFINITE)
			if waitErr != nil {
				return
			}
			var code uint32
			waitErr = windows.GetExitCodeProcess(info.Process, &code)
			if waitErr == nil && code != 0 {
				waitErr = fmt.Errorf("audio process exited with code %d", code)
			}
		})
		return waitErr
	}
	if err = ctx.Err(); err != nil {
		stop()
		_ = wait()
		return nil, err
	}
	return &audioProcess{pid: int(info.ProcessId), wait: wait, stop: stop}, nil
}
