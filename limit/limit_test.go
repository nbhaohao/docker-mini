package limit

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requireRoot 因为写 cgroup 接口文件、把进程 clone 进新资源组需要 root；在 VM 里用 sudo go test 跑。
func requireRoot(t *testing.T) {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.Uid != "0" {
		t.Skip("需要 root：orb -m backend sudo go test ./limit/... -run <name> -v")
	}
}

// TestHelperProcess 不是普通测试，是被各 spawn 函数通过重新执行本测试二进制拉起的子进程入口。
// GO_WANT_HELPER_PROCESS 开关避免 go test 正常跑测试列表时把它当成一条真正的测试执行。
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("HELPER_MODE") {
	case "memhog":
		memHog(200 * 1024 * 1024)
	case "cpuburn":
		cpuBurn()
	case "forkbomb":
		forkBomb(20)
	}
}

// memHog 分配 n 字节并逐页触碰，强迫内核给每一页分配真实物理内存（而不是只保留虚拟地址空间）。
// 如果所在 cgroup 的 memory.max 生效且 swap 被关闭，触碰到限额附近时这个进程会被内核 OOM killer 杀死。
func memHog(n int) {
	b := make([]byte, n)
	for i := 0; i < len(b); i += 4096 {
		b[i] = 1
	}
	fmt.Println("done, should not reach here if memory.max + swap off is enforced")
}

// cpuBurn 跑一段固定迭代次数的纯整数运算，耗时长短只取决于分到的 CPU 时间——正是 cpu.max 要限制的量。
func cpuBurn() {
	var n int64
	for i := int64(0); i < 3_000_000_000; i++ {
		n += i
	}
	fmt.Println(n)
}

// forkBomb 在收到一个字节的放行信号后（等测试把它 Join 进目标 cgroup），连续尝试 fork/exec attempts 个 `sleep 3` 子进程，
// 打印成功/失败计数，再等待全部子进程退出——用于观察 pids.max 生效时 fork 从某一刻开始持续失败。
// 注意：这个 forkbomb 进程自己（Go runtime）就占了好几个 task，pids.max 数的是任务数不是"业务进程数"。
func forkBomb(attempts int) {
	var ready [1]byte
	os.Stdin.Read(ready[:])

	var cmds []*exec.Cmd
	ok, fail := 0, 0
	for i := 0; i < attempts; i++ {
		c := exec.Command("sleep", "3")
		if err := c.Start(); err != nil {
			fail++
			continue
		}
		ok++
		cmds = append(cmds, c)
	}
	fmt.Printf("ok=%d fail=%d\n", ok, fail)
	for _, c := range cmds {
		c.Wait()
	}
}

func helperCommand(mode string) *exec.Cmd {
	c := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	c.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "HELPER_MODE="+mode)
	return c
}

// readEventCounter 从 path 这份 cgroup 接口文件（memory.events / pids.events）里取出某个字段的计数值。
func readEventCounter(t *testing.T, path, key string) int64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == key {
			v, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				t.Fatalf("parse %s value %q: %v", key, fields[1], err)
			}
			return v
		}
	}
	t.Fatalf("key %s not found in %s", key, path)
	return 0
}

func TestM02P2MemoryLimitOOMKills(t *testing.T) {
	requireRoot(t)
	path, err := Setup("test-mem", Limits{MemoryBytes: 50 * 1024 * 1024})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })

	cmd := helperCommand("memhog")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start memhog: %v", err)
	}
	if err := Join(path, cmd.Process.Pid); err != nil {
		t.Fatalf("Join: %v", err)
	}

	waitErr := cmd.Wait()
	if waitErr == nil {
		t.Fatalf("memhog should have been killed before finishing, output: %s", out.String())
	}
	if oomKill := readEventCounter(t, filepath.Join(path, "memory.events"), "oom_kill"); oomKill < 1 {
		t.Fatalf("memory.events oom_kill = %d, want >= 1", oomKill)
	}
}

func TestM02P3CPUQuotaThrottles(t *testing.T) {
	requireRoot(t)

	basePath, err := Setup("test-cpu-base", Limits{})
	if err != nil {
		t.Fatalf("Setup baseline: %v", err)
	}
	t.Cleanup(func() { os.Remove(basePath) })
	baseDur := runCPUBurnIn(t, basePath)

	limitPath, err := Setup("test-cpu-limit", Limits{CPUQuotaUS: 20000})
	if err != nil {
		t.Fatalf("Setup limited: %v", err)
	}
	t.Cleanup(func() { os.Remove(limitPath) })
	limitedDur := runCPUBurnIn(t, limitPath)

	if limitedDur < baseDur+baseDur/2 {
		t.Fatalf("cpu.max=20%% should take clearly longer: baseline=%v limited=%v", baseDur, limitedDur)
	}
}

func runCPUBurnIn(t *testing.T, path string) time.Duration {
	t.Helper()
	cmd := helperCommand("cpuburn")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start cpuburn: %v", err)
	}
	if err := Join(path, cmd.Process.Pid); err != nil {
		t.Fatalf("Join: %v", err)
	}
	start := time.Now()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cpuburn failed: %v, output: %s", err, out.String())
	}
	return time.Since(start)
}

func TestM02P4PIDsLimitCapsForkBomb(t *testing.T) {
	requireRoot(t)
	path, err := Setup("test-pids", Limits{PIDsMax: 12})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })

	cmd := helperCommand("forkbomb")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start forkbomb: %v", err)
	}
	if err := Join(path, cmd.Process.Pid); err != nil {
		t.Fatalf("Join: %v", err)
	}
	stdin.Write([]byte("go\n"))
	stdin.Close()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("forkbomb failed: %v, output: %s", err, out.String())
	}

	line := strings.TrimSpace(out.String())
	var ok, fail int
	if _, err := fmt.Sscanf(line, "ok=%d fail=%d", &ok, &fail); err != nil {
		t.Fatalf("parse forkbomb output %q: %v", line, err)
	}
	t.Logf("forkbomb result: ok=%d fail=%d", ok, fail)
	if fail == 0 {
		t.Fatalf("expected some fork attempts to fail once pids.max=12 is hit, got ok=%d fail=%d", ok, fail)
	}
	if maxHits := readEventCounter(t, filepath.Join(path, "pids.events"), "max"); maxHits < 1 {
		t.Fatalf("pids.events max = %d, want >= 1 (fork should have been blocked at least once)", maxHits)
	}
}
