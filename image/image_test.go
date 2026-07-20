package image

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/nbhaohao/docker-mini/isolate"
)

// unmountLazy 尽力卸载测试过程中挂的 overlay mount；MNT_DETACH 允许即使有残留引用也不阻塞卸载。
func unmountLazy(path string) {
	_ = syscall.Unmount(path, syscall.MNT_DETACH)
}

// requireRoot 因为挂载 overlayfs、pivot_root 都需要 root；在 VM 里用 sudo go test 跑。
func requireRoot(t *testing.T) {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.Uid != "0" {
		t.Skip("需要 root：orb -m backend sudo go test ./image/... -run <name> -v")
	}
}

// TestHelperProcess 不是普通测试，是被 runPivotHelper 通过重新执行本测试二进制拉起的子进程入口。
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	if os.Getenv("HELPER_MODE") != "pivot" {
		return
	}
	if err := PivotInto(os.Getenv("HELPER_ROOT")); err != nil {
		os.Stderr.WriteString("pivot failed: " + err.Error() + "\n")
		os.Exit(1)
	}
	data, err := os.ReadFile("/marker.txt")
	if err != nil {
		os.Stderr.WriteString("read after pivot failed: " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Stdout.Write(data)
	os.Exit(0)
}

// runPivotHelper 重新执行测试二进制，clone 进一个私有挂载 namespace（复用 m01 的 isolate.SysProcAttrFor），
// 二进制内部再走 TestHelperProcess -> PivotInto 完成换根。
func runPivotHelper(t *testing.T, merged string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"HELPER_MODE=pivot",
		"HELPER_ROOT="+merged,
	)
	cmd.SysProcAttr = isolate.SysProcAttrFor(isolate.NamespaceFlags{NS: true})
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestM03P2OverlayWritesGoToUpperOnly(t *testing.T) {
	requireRoot(t)
	lower, upper, work, merged := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(lower, "marker.txt"), []byte("hello-from-lower"), 0644); err != nil {
		t.Fatalf("seed lower: %v", err)
	}

	if err := MountOverlay(lower, upper, work, merged); err != nil {
		t.Fatalf("MountOverlay: %v", err)
	}
	t.Cleanup(func() { unmountLazy(merged) })

	if err := os.WriteFile(filepath.Join(merged, "container-write.txt"), []byte("written-in-container"), 0644); err != nil {
		t.Fatalf("write via merged: %v", err)
	}

	if _, err := os.Stat(filepath.Join(upper, "container-write.txt")); err != nil {
		t.Fatalf("new file should land in upper: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lower, "container-write.txt")); err == nil {
		t.Fatalf("new file must NOT appear in lower（镜像层不该被容器写入污染）")
	}

	got, err := os.ReadFile(filepath.Join(merged, "marker.txt"))
	if err != nil || string(got) != "hello-from-lower" {
		t.Fatalf("merged should still read through to lower's marker.txt, got %q err=%v", got, err)
	}
}

func TestM03P3PivotSwapsRoot(t *testing.T) {
	requireRoot(t)
	lower, upper, work, merged := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(lower, "marker.txt"), []byte("m03-pivot-marker"), 0644); err != nil {
		t.Fatalf("seed lower: %v", err)
	}
	if err := MountOverlay(lower, upper, work, merged); err != nil {
		t.Fatalf("MountOverlay: %v", err)
	}
	t.Cleanup(func() { unmountLazy(merged) })

	out, err := runPivotHelper(t, merged)
	if err != nil {
		t.Fatalf("pivot helper failed: %v, output: %s", err, out)
	}
	if strings.TrimSpace(out) != "m03-pivot-marker" {
		t.Fatalf("child should read merged's marker.txt at new root's /marker.txt, got %q", out)
	}

	hostGot, err := os.ReadFile(filepath.Join(merged, "marker.txt"))
	if err != nil || string(hostGot) != "m03-pivot-marker" {
		t.Fatalf("host view of merged must be unaffected after child's private-namespace pivot, got %q err=%v", hostGot, err)
	}
}
