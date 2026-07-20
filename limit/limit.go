package limit

import (
	"os"
	"path/filepath"
)

// cgroupRoot 是 mini-docker 所有资源组的父目录。它自己也是一个 cgroup（挂在 /sys/fs/cgroup 这棵统一层级下），
// 要把 memory/cpu/pids 控制器"下放"给自己的子目录，必须先在这个父目录的 cgroup.subtree_control 里显式启用。
const cgroupRoot = "/sys/fs/cgroup/mini-docker"

// cpuPeriodUS 是 cpu.max 的固定周期（微秒）；本课不开放自定义周期，只调 quota 部分。
const cpuPeriodUS = 100000

// Limits 声明一次 Setup 要施加哪些资源上限。字段为 0 表示这一维不设限。
// MemoryBytes → m02 P2（memory.max + OOM），CPUQuotaUS → P3（cpu.max + 压测对比），PIDsMax → P4（pids.max 防 fork 炸弹）。
type Limits struct {
	MemoryBytes int64
	CPUQuotaUS  int64
	PIDsMax     int64
}

// Setup 在 cgroupRoot 下创建（若不存在）名为 name 的资源组目录，按 l 写入对应的 cgroup v2 接口文件，返回这个组的路径。
func Setup(name string, l Limits) (path string, err error) {
	if err := os.MkdirAll(cgroupRoot, 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"), []byte("+memory +cpu +pids"), 0644); err != nil {
		return "", err
	}

	path = filepath.Join(cgroupRoot, name)
	if err := os.MkdirAll(path, 0755); err != nil {
		return "", err
	}
	return path, nil
}

// 你来实现（P2 起，逐 phase 往这个函数里加字段分支）：
// 1. 若 cgroupRoot 目录不存在，os.MkdirAll 创建它（0755）
// 2. 把 "+memory +cpu +pids" 写进 cgroupRoot/cgroup.subtree_control（os.WriteFile），
//    这样它的子目录才能看到 memory.max / cpu.max / pids.max 这些接口文件——重复写已启用的位不会报错
// 3. path = filepath.Join(cgroupRoot, name)；os.MkdirAll(path, 0755) 创建这个资源组
// 4.（P2）若 l.MemoryBytes > 0：
//    - 把 l.MemoryBytes 写进 path/memory.max（strconv.FormatInt 转成十进制字符串）
//    - 把 "0" 写进 path/memory.swap.max —— 不关 swap，超限时内核可能靠换出扛住，不一定触发 OOM kill
// 5.（P3）若 l.CPUQuotaUS > 0：把 "<quota> <cpuPeriodUS>" 写进 path/cpu.max
// 6.（P4）若 l.PIDsMax > 0：把 l.PIDsMax 写进 path/pids.max
// 7. 返回 path, nil

// Join 把 pid 这个进程写进 path 对应资源组的 cgroup.procs，使它以及它之后 fork 出的子进程都受这份限额约束。
func Join(path string, pid int) error

// 你来实现（P2）：
// 把 strconv.Itoa(pid) 写进 filepath.Join(path, "cgroup.procs")（os.WriteFile）
