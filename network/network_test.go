package network

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nbhaohao/docker-mini/isolate"
)

// requireRoot 因为 veth/bridge/nft/nsenter 都需要 CAP_NET_ADMIN；在 VM 里用 sudo go test 跑。
func requireRoot(t *testing.T) {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.Uid != "0" {
		t.Skip("需要 root：orb -m backend sudo go test ./network/... -run <name> -v")
	}
}

// nsenterNet 是测试专用的检查工具：直接从宿主 nsenter 进 pid 的 network namespace 跑一条命令，
// 用来"偷看"容器内部此刻的网络状态是否符合预期（真实 mini-docker 里这类命令由 network 包自己发起，
// 这里是测试在断言，走同一条 nsenter 路径）。
func nsenterNet(pid int, args ...string) ([]byte, error) {
	full := append([]string{"--net=/proc/" + strconv.Itoa(pid) + "/ns/net", "--"}, args...)
	return exec.Command("nsenter", full...).CombinedOutput()
}

// startNetnsHelper 用 CLONE_NEWNET 起一个长活的子进程（sleep 30），返回它的 pid 供
// AttachVeth/ConnectBridge/EnablePublish 操作；t.Cleanup 负责最后杀掉它。
func startNetnsHelper(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = isolate.SysProcAttrFor(isolate.NamespaceFlags{Net: true})
	if err := cmd.Start(); err != nil {
		t.Fatalf("start netns helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

func TestM04P2CtrVethConfigured(t *testing.T) {
	requireRoot(t)
	pid := startNetnsHelper(t)
	cfg := Config{HostVeth: "veth-t2", CtrVeth: "ceth-t2", ContainerCIDR: "10.220.1.2/24"}

	if err := AttachVeth(pid, cfg); err != nil {
		t.Fatalf("AttachVeth: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", cfg.HostVeth).Run() })

	addrOut, err := nsenterNet(pid, "ip", "-o", "addr", "show", cfg.CtrVeth)
	if err != nil {
		t.Fatalf("inspect ctr veth addr inside netns: %v (%s)", err, addrOut)
	}
	if !strings.Contains(string(addrOut), "10.220.1.2/24") {
		t.Fatalf("ctr veth should carry %s, got: %s", cfg.ContainerCIDR, addrOut)
	}

	linkOut, err := nsenterNet(pid, "ip", "-o", "link", "show", cfg.CtrVeth)
	if err != nil {
		t.Fatalf("inspect ctr veth link inside netns: %v (%s)", err, linkOut)
	}
	if !strings.Contains(string(linkOut), "UP") {
		t.Fatalf("ctr veth should be up after AttachVeth (namespace 里新网卡默认是 DOWN 的), got: %s", linkOut)
	}
}

func TestM04P3BridgeConnected(t *testing.T) {
	requireRoot(t)
	pid := startNetnsHelper(t)
	cfg := Config{
		HostVeth: "veth-t3", CtrVeth: "ceth-t3",
		BridgeName: "br-t3", BridgeCIDR: "10.220.2.1/24", ContainerCIDR: "10.220.2.2/24",
	}
	if err := AttachVeth(pid, cfg); err != nil {
		t.Fatalf("AttachVeth: %v", err)
	}
	if err := ConnectBridge(pid, cfg); err != nil {
		t.Fatalf("ConnectBridge: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("ip", "link", "del", cfg.HostVeth).Run()
		_ = exec.Command("ip", "link", "del", cfg.BridgeName).Run()
	})

	out, err := nsenterNet(pid, "ping", "-c1", "-W1", gatewayIP(cfg.BridgeCIDR))
	if err != nil {
		t.Fatalf("container should reach bridge gateway %s via ping: %v (%s)", gatewayIP(cfg.BridgeCIDR), err, out)
	}
	if !strings.Contains(string(out), "1 received") {
		t.Fatalf("ping should report 1 packet received, got: %s", out)
	}
}

func TestM04P4PublishReachesContainer(t *testing.T) {
	requireRoot(t)
	pid := startNetnsHelper(t)
	cfg := Config{
		HostVeth: "veth-t4", CtrVeth: "ceth-t4",
		BridgeName: "br-t4", BridgeCIDR: "10.220.3.1/24", ContainerCIDR: "10.220.3.2/24",
		HostPort: 17400, ContainerPort: 7400,
	}
	if err := AttachVeth(pid, cfg); err != nil {
		t.Fatalf("AttachVeth: %v", err)
	}
	if err := ConnectBridge(pid, cfg); err != nil {
		t.Fatalf("ConnectBridge: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("ip", "link", "del", cfg.HostVeth).Run()
		_ = exec.Command("ip", "link", "del", cfg.BridgeName).Run()
	})

	outFile, err := os.CreateTemp("", "m04-p4-listener-*.out")
	if err != nil {
		t.Fatalf("create temp log: %v", err)
	}
	defer os.Remove(outFile.Name())

	listener := exec.Command("nsenter", "--net=/proc/"+strconv.Itoa(pid)+"/ns/net", "--",
		"nc", "-l", "-p", strconv.Itoa(cfg.ContainerPort), "-k")
	listener.Stdout = outFile
	listener.Stderr = outFile
	if err := listener.Start(); err != nil {
		t.Fatalf("start container-side listener: %v", err)
	}
	t.Cleanup(func() { _ = listener.Process.Kill(); _ = listener.Wait() })
	time.Sleep(300 * time.Millisecond)

	if err := EnablePublish(cfg); err != nil {
		t.Fatalf("EnablePublish: %v", err)
	}
	t.Cleanup(func() { _ = Teardown(cfg) })

	// nc 客户端在这条 hairpin 路径上会等对端主动关闭连接，用 timeout 兜底，
	// 不据它的退出码判断成败——真正的断言是监听端有没有收到内容。
	send := exec.Command("timeout", "1", "bash", "-c",
		"echo m04-publish-test | nc 127.0.0.1 "+strconv.Itoa(cfg.HostPort))
	_ = send.Run()
	time.Sleep(300 * time.Millisecond)

	got, _ := os.ReadFile(outFile.Name())
	if !strings.Contains(string(got), "m04-publish-test") {
		t.Fatalf("published host port %d should reach container listener on %d, listener log: %q",
			cfg.HostPort, cfg.ContainerPort, got)
	}
}

func TestM04P4TeardownLeavesNoResidue(t *testing.T) {
	requireRoot(t)
	pid := startNetnsHelper(t)
	cfg := Config{
		HostVeth: "veth-t5", CtrVeth: "ceth-t5",
		BridgeName: "br-t5", BridgeCIDR: "10.220.4.1/24", ContainerCIDR: "10.220.4.2/24",
		HostPort: 17500, ContainerPort: 7500,
	}
	if err := AttachVeth(pid, cfg); err != nil {
		t.Fatalf("AttachVeth: %v", err)
	}
	if err := ConnectBridge(pid, cfg); err != nil {
		t.Fatalf("ConnectBridge: %v", err)
	}
	if err := EnablePublish(cfg); err != nil {
		t.Fatalf("EnablePublish: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", cfg.BridgeName).Run() })

	if err := Teardown(cfg); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	if err := exec.Command("ip", "link", "show", cfg.HostVeth).Run(); err == nil {
		t.Fatalf("HostVeth %s should be gone after Teardown, but ip link show succeeded", cfg.HostVeth)
	}
	if err := exec.Command("nft", "list", "table", "ip", natTableName(cfg)).Run(); err == nil {
		t.Fatalf("nat table %s should be gone after Teardown, but nft list succeeded", natTableName(cfg))
	}
}
