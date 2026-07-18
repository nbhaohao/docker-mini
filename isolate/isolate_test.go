package isolate

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"testing"
)

// requireRoot 因为 clone(2) 开 namespace 需要 CAP_SYS_ADMIN；在 VM 里用 sudo go test 跑。
func requireRoot(t *testing.T) {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.Uid != "0" {
		t.Skip("需要 root：orb -m backend sudo go test ./isolate/... -run <name> -v")
	}
}

// TestHelperProcess 不是普通测试，是被 runHelper 通过重新执行本测试二进制拉起的容器子进程入口。
// GO_WANT_HELPER_PROCESS 开关避免 go test 正常跑测试列表时把它当成一条真正的测试执行。
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	flags := NamespaceFlags{
		UTS: os.Getenv("HELPER_UTS") == "1",
		PID: os.Getenv("HELPER_PID") == "1",
		NS:  os.Getenv("HELPER_NS") == "1",
	}
	dashdash := -1
	for i, a := range os.Args {
		if a == "--" {
			dashdash = i
			break
		}
	}
	if dashdash < 0 || dashdash+1 >= len(os.Args) {
		fmt.Fprintln(os.Stderr, "helper: missing target command after --")
		os.Exit(2)
	}
	target := os.Args[dashdash+1:]
	if err := EnterAndExec(flags, target); err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		os.Exit(1)
	}
}

// runHelper 用 SysProcAttrFor(f) 把重新执行的测试二进制直接 clone 进新 namespace，
// 二进制内部再走 TestHelperProcess -> EnterAndExec 完成 hostname/proc 与最终 execve。
func runHelper(t *testing.T, f NamespaceFlags, target []string) (string, error) {
	t.Helper()
	args := []string{"-test.run=TestHelperProcess", "--"}
	args = append(args, target...)
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"HELPER_UTS="+boolFlag(f.UTS),
		"HELPER_PID="+boolFlag(f.PID),
		"HELPER_NS="+boolFlag(f.NS),
	)
	cmd.SysProcAttr = SysProcAttrFor(f)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func boolFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func TestM01P2HostnameIsolated(t *testing.T) {
	requireRoot(t)
	out, err := runHelper(t, NamespaceFlags{UTS: true}, []string{"hostname"})
	if err != nil {
		t.Fatalf("helper failed: %v\noutput: %s", err, out)
	}
	if got := strings.TrimSpace(out); got != "mini-docker" {
		t.Fatalf("child hostname = %q, want mini-docker", got)
	}
	hostOut, err := exec.Command("hostname").Output()
	if err != nil {
		t.Fatalf("read host hostname: %v", err)
	}
	if strings.TrimSpace(string(hostOut)) == "mini-docker" {
		t.Fatal("host hostname leaked into mini-docker: CLONE_NEWUTS isolation broken")
	}
}

func TestM01P3SelfPidIsOne(t *testing.T) {
	requireRoot(t)
	out, err := runHelper(t, NamespaceFlags{UTS: true, PID: true}, []string{"sh", "-c", "echo $$"})
	if err != nil {
		t.Fatalf("helper failed: %v\noutput: %s", err, out)
	}
	got, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil || got != 1 {
		t.Fatalf("child self pid = %q, want 1", strings.TrimSpace(out))
	}
}

func TestM01P3ProcStillLeaksHostWithoutRemount(t *testing.T) {
	requireRoot(t)
	out, err := runHelper(t, NamespaceFlags{UTS: true, PID: true}, []string{"ps", "-e"})
	if err != nil {
		t.Fatalf("ps -e should still succeed without remount: %v\noutput: %s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// 未 remount /proc 时 ps 读到的是宿主整份进程表：数据行(去掉表头) > 3 才说明确实没被隔离。
	if len(lines) <= 3 {
		t.Fatalf("ps -e output too short (%d lines) to prove host /proc leaked:\n%s", len(lines), out)
	}
}

func TestM01P4ProcIsolatedAfterRemount(t *testing.T) {
	requireRoot(t)
	out, err := runHelper(t, NamespaceFlags{UTS: true, PID: true, NS: true}, []string{"ps", "-e"})
	if err != nil {
		t.Fatalf("helper failed: %v\noutput: %s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ps -e should show exactly 1 header + 1 data row after remount, got %d lines:\n%s", len(lines), out)
	}
	fields := strings.Fields(lines[1])
	if len(fields) == 0 || fields[0] != "1" {
		t.Fatalf("ps -e data row should report pid 1, got: %q", lines[1])
	}
}
