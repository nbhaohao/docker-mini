package isolate

import (
	"os"
	"os/exec"
	"syscall"
)

// NamespaceFlags 声明本次 clone 要打开哪些 namespace。三个字段分别对应 m01 三个实现 phase：
// UTS → P2（hostname 隔离），PID → P3（PID 隔离），NS → P4（挂载隔离 + remount /proc）。
type NamespaceFlags struct {
	UTS bool
	PID bool
	NS  bool
}

// SysProcAttrFor 把 NamespaceFlags 换算成 exec.Cmd 需要的 SysProcAttr。
// Cloneflags 决定 clone(2) 给即将创建的子进程新开哪些 namespace。
func SysProcAttrFor(f NamespaceFlags) *syscall.SysProcAttr {
	var flags uintptr
	if f.UTS {
		flags |= syscall.CLONE_NEWUTS
	}
	return &syscall.SysProcAttr{Cloneflags: flags}
}

// 你来实现（P2）：
// 1. 声明一个 uintptr 变量 flags，初值 0
// 2. 若 f.UTS 为 true，flags |= syscall.CLONE_NEWUTS
// 3. 用 flags 构造 &syscall.SysProcAttr{Cloneflags: flags} 并返回

// EnterAndExec 是子进程刚进入新 namespace 后要做的全部事情：先按 flags 完成必要准备，
// 再把当前进程换成 target 这个目标命令。
func EnterAndExec(f NamespaceFlags, target []string) error {
	if f.UTS {
		if err := syscall.Sethostname([]byte("mini-docker")); err != nil {
			return err
		}
	}
	path, err := exec.LookPath(target[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, target, os.Environ())
}

// 你来实现（P2）：
// 1. 若 f.UTS 为 true，调用 syscall.Sethostname 把 hostname 设为 "mini-docker"
// 2. 用 exec.LookPath(target[0]) 找到目标命令的可执行文件完整路径
// 3. 用 syscall.Exec(path, target, os.Environ()) 把当前进程「换像」成目标命令，
//    而不是用 exec.Command(...).Run() 再 fork 一个新进程——原因见 P1 热身。
//    返回 syscall.Exec 的 error（正常情况下这行代码不会返回，因为进程已经被换像）。
