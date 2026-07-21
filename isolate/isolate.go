package isolate

import (
	"os"
	"os/exec"
	"syscall"
)

// NamespaceFlags 声明本次 clone 要打开哪些 namespace。四个字段分别对应四个实现 phase：
// UTS → m01 P2（hostname 隔离），PID → m01 P3（PID 隔离），NS → m01 P4（挂载隔离 + remount /proc），
// Net → m04 P1（网络栈隔离：新进程只看得到自己的一份网卡/路由/端口，而不是宿主那份）。
type NamespaceFlags struct {
	UTS bool
	PID bool
	NS  bool
	Net bool
}

// SysProcAttrFor 把 NamespaceFlags 换算成 exec.Cmd 需要的 SysProcAttr。
// Cloneflags 决定 clone(2) 给即将创建的子进程新开哪些 namespace。
func SysProcAttrFor(f NamespaceFlags) *syscall.SysProcAttr {
	var flags uintptr
	if f.UTS {
		flags |= syscall.CLONE_NEWUTS
	}
	if f.PID {
		flags |= syscall.CLONE_NEWPID
	}
	attr := &syscall.SysProcAttr{Cloneflags: flags}
	if f.NS {
		attr.Cloneflags |= syscall.CLONE_NEWNS
		attr.Unshareflags = syscall.CLONE_NEWNS
	}
	return attr
}

// 你来实现（m04 P1）：
// 1. 若 f.Net 为 true，flags |= syscall.CLONE_NEWNET
// 2. 加在 UTS/PID 判断之后、attr 构造之前即可，和 NS 那段不一样：Net 不需要额外的 Unshareflags

// EnterAndExec 是子进程刚进入新 namespace 后要做的全部事情：先按 flags 完成必要准备，
// 再把当前进程换成 target 这个目标命令。
func EnterAndExec(f NamespaceFlags, target []string) error {
	if f.UTS {
		if err := syscall.Sethostname([]byte("mini-docker")); err != nil {
			return err
		}
	}
	if f.PID && f.NS {
		if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
			return err
		}
	}
	path, err := exec.LookPath(target[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, target, os.Environ())
}

// 你来实现（m04 P1）：
// 1. 若 f.Net 为 true，新 network namespace 里连回环网卡默认都是 DOWN 的，得手动拉起来，
//    否则连 127.0.0.1 都不通：
//        lo := exec.Command("ip", "link", "set", "lo", "up")
//        lo.Stdin, lo.Stdout, lo.Stderr = os.Stdin, os.Stdout, os.Stderr
//        if err := lo.Run(); err != nil { return err }
// 2. 放在 exec.LookPath 之前执行（这条命令跑完就结束了，不影响后面的换像）。
// 3. 那三个流为什么必须显式接上：os/exec 对没设置的流会去打开 /dev/null——而这行代码此刻
//    已经在 m03 换根之后的 alpine rootfs 里，那套精简 rootfs 根本没有 /dev/null，
//    默认行为会直接报 open /dev/null: no such file or directory，而且报错完全不提网络。
//    （m01/m04 的单元测试跑在宿主 rootfs 上，有 /dev/null，只有真容器 e2e 会撞上这个坑。）
