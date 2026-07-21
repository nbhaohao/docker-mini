package main

import (
	"bytes"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gatewayIPForTest 是 main.go 里固定的那张网桥（mini-docker0）的地址，容器的默认网关。
const gatewayIPForTest = "10.200.0.1"

// requireRoot 因为 mini-docker run 要开 namespace、挂 overlayfs、写 cgroup 接口文件，全部需要 root。
func requireRoot(t *testing.T) {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.Uid != "0" {
		t.Skip("需要 root：orb -m backend sudo go test ./cmd/mini-docker/... -run <name> -v")
	}
}

// buildMiniDocker 编译出真正的 mini-docker 二进制，返回它的路径。
func buildMiniDocker(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mini-docker")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build mini-docker: %v, output: %s", err, out)
	}
	return bin
}

// alpineRootfs 返回 P1 缓存好的 alpine rootfs 绝对路径，没缓存就跳过。
func alpineRootfs(t *testing.T) string {
	t.Helper()
	rootfs := filepath.Join("..", "..", ".cache", "alpine-rootfs")
	if _, err := os.Stat(filepath.Join(rootfs, "bin", "busybox")); err != nil {
		t.Skip("先跑 m03 P1 的 orb -m backend sh scripts/checks-m03-p1.sh 缓存 alpine rootfs")
	}
	abs, err := filepath.Abs(rootfs)
	if err != nil {
		t.Fatalf("resolve rootfs path: %v", err)
	}
	return abs
}

// TestM04P4RunNetworkedContainerEndToEnd 是全课收官验收：一条 mini-docker run 同时带上
// 四件套——namespace（m01）+ cgroup（m02）+ overlayfs/pivot_root（m03）+ 网络（m04），
// 容器内部必须真的拿到网桥网段的地址，并且能 ping 通网桥（宿主）。
func TestM04P4RunNetworkedContainerEndToEnd(t *testing.T) {
	requireRoot(t)
	bin := buildMiniDocker(t)
	absRootfs := alpineRootfs(t)

	cmd := exec.Command(bin, "run", "--uts", "--pid", "--ns", "--net", "--rootfs", absRootfs,
		"--", "/bin/sh", "-c", "ip -o addr show; ping -c1 -W2 "+gatewayIPForTest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mini-docker run --net failed: %v, output: %s", err, out)
	}

	got := string(out)
	if !strings.Contains(got, "10.200.0.") {
		t.Fatalf("容器内应该拿到 10.200.0.0/24 网段的地址（AttachVeth 配的），实际输出: %s", got)
	}
	if !strings.Contains(got, "1 packets received") {
		t.Fatalf("容器应该能 ping 通网桥 %s（ConnectBridge 接线 + 默认路由），实际输出: %s", gatewayIPForTest, got)
	}
	if err := exec.Command("ip", "link", "show", "mini-docker0").Run(); err != nil {
		t.Fatalf("网桥 mini-docker0 是共享资源，容器退出后应该还在")
	}
	leftover, _ := exec.Command("sh", "-c", "ip -o link show | grep -c 'veth-[0-9]'").Output()
	if strings.TrimSpace(string(leftover)) != "0" {
		t.Fatalf("容器退出后不该残留 veth，实际还剩 %s 个", strings.TrimSpace(string(leftover)))
	}
}

// TestM04P4RunPublishedPortEndToEnd 验证 -p：宿主用 127.0.0.1:<宿主端口> 能连到容器里
// 真实监听的进程——DNAT + hairpin masquerade 这条完整链路在真容器上走通。
func TestM04P4RunPublishedPortEndToEnd(t *testing.T) {
	requireRoot(t)
	bin := buildMiniDocker(t)
	absRootfs := alpineRootfs(t)

	cmd := exec.Command(bin, "run", "--uts", "--pid", "--ns", "--net", "--rootfs", absRootfs,
		"-p", "18400:8400", "--", "/bin/sh", "-c", "nc -l -p 8400")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start container: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	// 容器内的 nc 需要一点时间起来，重试到通为止（最多 ~3s）。
	var sent bool
	for i := 0; i < 15; i++ {
		time.Sleep(200 * time.Millisecond)
		send := exec.Command("timeout", "1", "bash", "-c", "echo m04-e2e-publish | nc 127.0.0.1 18400")
		if err := send.Run(); err == nil {
			sent = true
			break
		}
	}
	if !sent {
		t.Fatalf("宿主始终连不上发布端口 18400，容器输出: %s", buf.String())
	}
	_ = cmd.Wait()

	if !strings.Contains(buf.String(), "m04-e2e-publish") {
		t.Fatalf("容器里的监听进程应该收到宿主经发布端口发来的内容，实际容器输出: %q", buf.String())
	}
}

// TestM03P4RunAlpineShellEndToEnd 是三个模块的收官验收：编译出 mini-docker 二进制，
// 真的用它跑一次 `alpine sh`，证明 namespace（m01）+ cgroup（m02）+ overlayfs/pivot_root（m03）三件套已经拼成一个能用的容器。
func TestM03P4RunAlpineShellEndToEnd(t *testing.T) {
	requireRoot(t)

	absRootfs := alpineRootfs(t)
	bin := buildMiniDocker(t)

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
