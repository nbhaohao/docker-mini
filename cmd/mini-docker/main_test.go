package main

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// requireRoot 因为 mini-docker run 要开 namespace、挂 overlayfs、写 cgroup 接口文件，全部需要 root。
func requireRoot(t *testing.T) {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.Uid != "0" {
		t.Skip("需要 root：orb -m backend sudo go test ./cmd/mini-docker/... -run <name> -v")
	}
}

// TestM03P4RunAlpineShellEndToEnd 是三个模块的收官验收：编译出 mini-docker 二进制，
// 真的用它跑一次 `alpine sh`，证明 namespace（m01）+ cgroup（m02）+ overlayfs/pivot_root（m03）三件套已经拼成一个能用的容器。
func TestM03P4RunAlpineShellEndToEnd(t *testing.T) {
	requireRoot(t)

	rootfs := filepath.Join("..", "..", ".cache", "alpine-rootfs")
	if _, err := os.Stat(filepath.Join(rootfs, "bin", "busybox")); err != nil {
		t.Skip("先跑 P1 的 orb -m backend sh scripts/checks-m03-p1.sh 缓存 alpine rootfs")
	}
	absRootfs, err := filepath.Abs(rootfs)
	if err != nil {
		t.Fatalf("resolve rootfs path: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "mini-docker")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build mini-docker: %v, output: %s", err, out)
	}

	cmd := exec.Command(bin, "run", "--uts", "--pid", "--ns", "--rootfs", absRootfs,
		"--", "/bin/sh", "-c", "echo capstone-hello; cat /etc/os-release; hostname")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mini-docker run failed: %v, output: %s", err, out)
	}

	got := string(out)
	if !strings.Contains(got, "capstone-hello") {
		t.Fatalf("expected shell command to actually run inside the container, got: %s", got)
	}
	if !strings.Contains(got, "Alpine Linux") {
		t.Fatalf("expected /etc/os-release to come from the pivoted alpine rootfs, not the host, got: %s", got)
	}
	if !strings.Contains(got, "mini-docker") {
		t.Fatalf("expected hostname override from m01 to still apply, got: %s", got)
	}
}
