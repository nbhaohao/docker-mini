// 已就位（AI 生成）：run/child 重新执行调度 + 参数解析，纯样板，不含 namespace/cgroup/rootfs 机制本身。
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/nbhaohao/docker-mini/image"
	"github.com/nbhaohao/docker-mini/isolate"
	"github.com/nbhaohao/docker-mini/limit"
)

// defaultLimits 是 `run --rootfs` 走完整容器路径时套用的默认资源上限：够一个交互 shell 用，
// 只用来证明三件套确实拼在一起了，不是本课要精调的数值（m02 已经深入讲过每个字段的语义）。
var defaultLimits = limit.Limits{MemoryBytes: 256 * 1024 * 1024, CPUQuotaUS: 50000, PIDsMax: 64}

func parseFlags(args []string) (f isolate.NamespaceFlags, rootfs, pivot string, rest []string) {
	i := 0
	for ; i < len(args); i++ {
		switch args[i] {
		case "--uts":
			f.UTS = true
		case "--pid":
			f.PID = true
		case "--ns":
			f.NS = true
		case "--rootfs":
			i++
			rootfs = args[i]
		case "--pivot":
			i++
			pivot = args[i]
		case "--":
			i++
			return f, rootfs, pivot, args[i:]
		default:
			return f, rootfs, pivot, args[i:]
		}
	}
	return f, rootfs, pivot, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mini-docker run|child [--uts] [--pid] [--ns] [--rootfs <dir>] -- <cmd> [args...]")
		os.Exit(2)
	}
	flags, rootfs, pivot, rest := parseFlags(os.Args[2:])
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "missing target command after --")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		runMode(flags, rootfs, rest)
	case "child":
		childMode(flags, pivot, rest)
	default:
		fmt.Fprintln(os.Stderr, "unknown mode:", os.Args[1])
		os.Exit(2)
	}
}

// runMode 是宿主侧的编排入口：--rootfs 给定时，先挂好 overlay 镜像层、算出 cgroup 限额组，
// 再把整个 mini-docker 二进制以 child 模式重新执行进 namespace，最后把子进程 pid 加入 cgroup。
func runMode(f isolate.NamespaceFlags, rootfs string, target []string) {
	self, err := os.Executable()
	must(err)

	childArgs := append([]string{"child"}, flagArgs(f)...)

	var merged, runtimeDir, cgroupPath string
	if rootfs != "" {
		runtimeDir, err = os.MkdirTemp("", "mini-docker-run-")
		must(err)
		merged, err = image.PrepareContainerRoot(runtimeDir, rootfs)
		must(err)
		childArgs = append(childArgs, "--pivot", merged)
		defer func() {
			_ = syscall.Unmount(merged, syscall.MNT_DETACH)
			_ = os.RemoveAll(runtimeDir)
		}()

		cgroupPath, err = limit.Setup(filepath.Base(runtimeDir), defaultLimits)
		must(err)
		defer func() { _ = os.Remove(cgroupPath) }()
	}

	childArgs = append(childArgs, "--")
	childArgs = append(childArgs, target...)
	cmd := exec.Command(self, childArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = isolate.SysProcAttrFor(f)

	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
	if cgroupPath != "" {
		must(limit.Join(cgroupPath, cmd.Process.Pid))
	}
	if err := cmd.Wait(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
}

// childMode 是子进程侧的编排入口：先（若给了 --pivot）换根到镜像层，再走 m01 的 EnterAndExec
// 完成 hostname/挂载 proc/最终换像执行，顺序固定不能反：换根必须先于换像。
func childMode(f isolate.NamespaceFlags, pivot string, target []string) {
	if pivot != "" {
		if err := image.PivotInto(pivot); err != nil {
			fmt.Fprintln(os.Stderr, "child: pivot:", err)
			os.Exit(1)
		}
	}
	if err := isolate.EnterAndExec(f, target); err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(1)
	}
}

func flagArgs(f isolate.NamespaceFlags) []string {
	var out []string
	if f.UTS {
		out = append(out, "--uts")
	}
	if f.PID {
		out = append(out, "--pid")
	}
	if f.NS {
		out = append(out, "--ns")
	}
	return out
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
