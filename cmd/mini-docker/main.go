// 已就位（AI 生成）：run/child 重新执行调度 + 参数解析，纯样板，不含 namespace/cgroup/rootfs 机制本身。
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/nbhaohao/docker-mini/image"
	"github.com/nbhaohao/docker-mini/isolate"
	"github.com/nbhaohao/docker-mini/limit"
	"github.com/nbhaohao/docker-mini/network"
)

// bridgeName / bridgeCIDR 是本课固定的那张网桥，作用等同于 Docker 的 docker0：
// 所有容器都接进同一张网桥、共用同一个网段，网桥地址就是容器的默认网关。
// 网桥是跨容器共享的公共资源，容器退出时只拆自己那对 veth，不拆网桥（Docker 同样不拆 docker0）。
const (
	bridgeName = "mini-docker0"
	bridgeCIDR = "10.200.0.1/24"
)

// containerNetConfig 按当前 run 进程的 pid 派生这个容器的网卡名与地址。
// 地址只取 pid 的低位当主机号——够本课「同时跑一两个容器」的场景用，真正的 IPAM
// （地址池分配 + 冲突检测 + 释放）不在本课范围内。
func containerNetConfig(publish string) network.Config {
	pid := os.Getpid()
	cfg := network.Config{
		HostVeth:      fmt.Sprintf("veth-%d", pid),
		CtrVeth:       fmt.Sprintf("ceth-%d", pid),
		BridgeName:    bridgeName,
		BridgeCIDR:    bridgeCIDR,
		ContainerCIDR: fmt.Sprintf("10.200.0.%d/24", 2+pid%200),
	}
	if publish != "" {
		hostPort, containerPort, err := parsePublish(publish)
		must(err)
		cfg.HostPort, cfg.ContainerPort = hostPort, containerPort
	}
	return cfg
}

// parsePublish 解析 -p 的 "宿主端口:容器端口" 写法，对应 docker run -p 的最简形式。
func parsePublish(spec string) (hostPort, containerPort int, err error) {
	host, container, found := strings.Cut(spec, ":")
	if !found {
		return 0, 0, fmt.Errorf("-p 需要 <宿主端口>:<容器端口> 格式，收到 %q", spec)
	}
	if hostPort, err = strconv.Atoi(host); err != nil {
		return 0, 0, fmt.Errorf("-p 宿主端口不是数字: %q", host)
	}
	if containerPort, err = strconv.Atoi(container); err != nil {
		return 0, 0, fmt.Errorf("-p 容器端口不是数字: %q", container)
	}
	return hostPort, containerPort, nil
}

// defaultLimits 是 `run --rootfs` 走完整容器路径时套用的默认资源上限：够一个交互 shell 用，
// 只用来证明三件套确实拼在一起了，不是本课要精调的数值（m02 已经深入讲过每个字段的语义）。
var defaultLimits = limit.Limits{MemoryBytes: 256 * 1024 * 1024, CPUQuotaUS: 50000, PIDsMax: 64}

func parseFlags(args []string) (f isolate.NamespaceFlags, rootfs, pivot, publish string, rest []string) {
	i := 0
	for ; i < len(args); i++ {
		switch args[i] {
		case "--uts":
			f.UTS = true
		case "--pid":
			f.PID = true
		case "--ns":
			f.NS = true
		case "--net":
			f.Net = true
		case "--rootfs":
			i++
			rootfs = args[i]
		case "--pivot":
			i++
			pivot = args[i]
		case "-p":
			i++
			publish = args[i]
		case "--":
			i++
			return f, rootfs, pivot, publish, args[i:]
		default:
			return f, rootfs, pivot, publish, args[i:]
		}
	}
	return f, rootfs, pivot, publish, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mini-docker run|child [--uts] [--pid] [--ns] [--net] [--rootfs <dir>] [-p <宿主端口>:<容器端口>] -- <cmd> [args...]")
		os.Exit(2)
	}
	flags, rootfs, pivot, publish, rest := parseFlags(os.Args[2:])
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "missing target command after --")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		runMode(flags, rootfs, publish, rest)
	case "child":
		childMode(flags, pivot, rest)
	default:
		fmt.Fprintln(os.Stderr, "unknown mode:", os.Args[1])
		os.Exit(2)
	}
}

// runMode 是宿主侧的编排入口：--rootfs 给定时，先挂好 overlay 镜像层、算出 cgroup 限额组，
// 再把整个 mini-docker 二进制以 child 模式重新执行进 namespace，最后把子进程 pid 加入 cgroup。
func runMode(f isolate.NamespaceFlags, rootfs, publish string, target []string) {
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

	// 网络布线全部由宿主侧完成，而它需要子进程的 pid——所以子进程必须先诞生。但这样就有了
	// 一个竞态：子进程可能在网卡还没接好时就 execve 跑起了用户命令，看到一个只有 lo 的空网络。
	// 解决办法是一根同步管道：子进程以 fd 3 拿到读端，布线完成前它会阻塞在这条读上，
	// 宿主布线完再写一个字节放行。真实的 runc 用的也是同一套「pipe 同步」套路。
	var netSync *os.File
	if f.Net {
		syncR, syncW, pipeErr := os.Pipe()
		must(pipeErr)
		cmd.ExtraFiles = []*os.File{syncR}
		netSync = syncW
		defer syncR.Close()
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
	if cgroupPath != "" {
		must(limit.Join(cgroupPath, cmd.Process.Pid))
	}
	var netCfg network.Config
	if f.Net {
		netCfg = containerNetConfig(publish)
		must(network.AttachVeth(cmd.Process.Pid, netCfg))
		must(network.ConnectBridge(cmd.Process.Pid, netCfg))
		if netCfg.HostPort != 0 {
			must(network.EnablePublish(netCfg))
		}
		_, _ = netSync.Write([]byte{1})
		_ = netSync.Close()
	}

	waitErr := cmd.Wait()
	// 容器退出后拆掉属于它的那对 veth 和那张 nat 表（网桥留着，下一个容器还要用）。
	// 注意这里不能用 defer：下面出错时走的是 os.Exit，defer 不会被执行，网卡会残留。
	if f.Net {
		_ = network.Teardown(netCfg)
	}
	if waitErr != nil {
		fmt.Fprintln(os.Stderr, "run:", waitErr)
		os.Exit(1)
	}
}

// childMode 是子进程侧的编排入口：先（若给了 --pivot）换根到镜像层，再走 m01 的 EnterAndExec
// 完成 hostname/挂载 proc/最终换像执行，顺序固定不能反：换根必须先于换像。
func childMode(f isolate.NamespaceFlags, pivot string, target []string) {
	// 开了 --net 时，宿主还在给这个 netns 接网线：先在同步管道（fd 3）上阻塞等一个字节，
	// 等宿主布线完成再继续，避免用户命令跑起来时看到一个只有 lo 的空网络。
	if f.Net {
		syncPipe := os.NewFile(3, "netns-sync")
		if syncPipe != nil {
			buf := make([]byte, 1)
			_, _ = syncPipe.Read(buf)
			_ = syncPipe.Close()
		}
	}
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
	if f.Net {
		out = append(out, "--net")
	}
	return out
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
