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

// TestM04P3ConnectBridgeIsIdempotent 盯住"网桥是共享资源"这条：第二个容器接进同一张网桥时，
// ConnectBridge 会被再调一次——此时网桥已经存在，无脑再 ip link add 会直接失败（File exists），
// 于是第二个容器永远起不来。
func TestM04P3ConnectBridgeIsIdempotent(t *testing.T) {
	requireRoot(t)
	firstPid := startNetnsHelper(t)
	secondPid := startNetnsHelper(t)
	first := Config{
		HostVeth: "veth-t8", CtrVeth: "ceth-t8",
		BridgeName: "br-t8", BridgeCIDR: "10.220.7.1/24", ContainerCIDR: "10.220.7.2/24",
	}
	second := Config{
		HostVeth: "veth-u8", CtrVeth: "ceth-u8",
		BridgeName: "br-t8", BridgeCIDR: "10.220.7.1/24", ContainerCIDR: "10.220.7.3/24",
	}
	t.Cleanup(func() {
		_ = exec.Command("ip", "link", "del", first.HostVeth).Run()
		_ = exec.Command("ip", "link", "del", second.HostVeth).Run()
		_ = exec.Command("ip", "link", "del", first.BridgeName).Run()
	})

	for _, c := range []struct {
		pid int
		cfg Config
	}{{firstPid, first}, {secondPid, second}} {
		if err := AttachVeth(c.pid, c.cfg); err != nil {
			t.Fatalf("AttachVeth %s: %v", c.cfg.CtrVeth, err)
		}
		if err := ConnectBridge(c.pid, c.cfg); err != nil {
			t.Fatalf("ConnectBridge %s 应该在网桥已存在时也能成功（网桥是共用资源，第二个容器只接线不重建）: %v", c.cfg.CtrVeth, err)
		}
	}

	out, err := nsenterNet(secondPid, "ping", "-c1", "-W1", gatewayIP(second.BridgeCIDR))
	if err != nil || !strings.Contains(string(out), "1 received") {
		t.Fatalf("第二个容器也应该能 ping 通同一张网桥: %v (%s)", err, out)
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

// attachPeerToBridge 把另一个 netns 也接到同一张网桥上，用来在测试里扮演「另一台机器」。
// 这是测试自己的接线工具，不属于课程 API。
func attachPeerToBridge(t *testing.T, pid int, peer Config, bridgeName string) {
	t.Helper()
	if err := AttachVeth(pid, peer); err != nil {
		t.Fatalf("AttachVeth peer: %v", err)
	}
	for _, args := range [][]string{
		{"link", "set", peer.HostVeth, "master", bridgeName},
		{"link", "set", peer.HostVeth, "up"},
	} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v (%s)", args, err, out)
		}
	}
}

// startListener 在 pid 所在 netns 里起一个 nc 监听，返回它的日志文件路径。
func startListener(t *testing.T, pid, port int, extraArgs ...string) string {
	t.Helper()
	logFile, err := os.CreateTemp("", "m04-listener-*.out")
	if err != nil {
		t.Fatalf("create temp log: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(logFile.Name()) })
	args := append([]string{"--net=/proc/" + strconv.Itoa(pid) + "/ns/net", "--", "nc"}, extraArgs...)
	args = append(args, "-l", "-p", strconv.Itoa(port), "-k")
	listener := exec.Command("nsenter", args...)
	listener.Stdout = logFile
	listener.Stderr = logFile
	if err := listener.Start(); err != nil {
		t.Fatalf("start listener in netns %d: %v", pid, err)
	}
	t.Cleanup(func() { _ = listener.Process.Kill(); _ = listener.Wait() })
	time.Sleep(300 * time.Millisecond)
	return logFile.Name()
}

// TestM04P4PublishFromPeerNetns 走的是 PREROUTING 那条 DNAT：包从另一个 netns（扮演「外部机器」）
// 经网卡进入宿主，而不是宿主自己发起的。上面那条 127.0.0.1 的测试走的是 OUTPUT 链——只挂 output
// 一条 DNAT 时它照样能过，所以「两个钩子各挂一条」这句话必须由这条测试来兜住。
func TestM04P4PublishFromPeerNetns(t *testing.T) {
	requireRoot(t)
	ctrPid := startNetnsHelper(t)
	peerPid := startNetnsHelper(t)
	cfg := Config{
		HostVeth: "veth-t6", CtrVeth: "ceth-t6",
		BridgeName: "br-t6", BridgeCIDR: "10.220.5.1/24", ContainerCIDR: "10.220.5.2/24",
		HostPort: 17600, ContainerPort: 7600,
	}
	peer := Config{HostVeth: "veth-p6", CtrVeth: "ceth-p6", ContainerCIDR: "10.220.5.3/24"}

	if err := AttachVeth(ctrPid, cfg); err != nil {
		t.Fatalf("AttachVeth: %v", err)
	}
	if err := ConnectBridge(ctrPid, cfg); err != nil {
		t.Fatalf("ConnectBridge: %v", err)
	}
	attachPeerToBridge(t, peerPid, peer, cfg.BridgeName)
	t.Cleanup(func() {
		_ = exec.Command("ip", "link", "del", cfg.HostVeth).Run()
		_ = exec.Command("ip", "link", "del", peer.HostVeth).Run()
		_ = exec.Command("ip", "link", "del", cfg.BridgeName).Run()
	})

	logPath := startListener(t, ctrPid, cfg.ContainerPort)
	if err := EnablePublish(cfg); err != nil {
		t.Fatalf("EnablePublish: %v", err)
	}
	t.Cleanup(func() { _ = Teardown(cfg) })

	send := exec.Command("nsenter", "--net=/proc/"+strconv.Itoa(peerPid)+"/ns/net", "--",
		"timeout", "1", "bash", "-c",
		"echo m04-prerouting-test | nc "+gatewayIP(cfg.BridgeCIDR)+" "+strconv.Itoa(cfg.HostPort))
	_ = send.Run()
	time.Sleep(300 * time.Millisecond)

	got, _ := os.ReadFile(logPath)
	if !strings.Contains(string(got), "m04-prerouting-test") {
		t.Fatalf("外部 netns 访问 %s:%d 应该被 prerouting DNAT 送进容器 %d，listener log: %q",
			gatewayIP(cfg.BridgeCIDR), cfg.HostPort, cfg.ContainerPort, got)
	}
}

// TestM04P4EgressMasquerades 验证「容器出网」那条 saddr masquerade：容器访问另一个网段的
// 「外部主机」时，对方看到的源地址必须已经被换成宿主的出口地址；少了这条规则，对方连回包
// 该往哪送都不知道（私网地址不可达），连接根本建不起来。
func TestM04P4EgressMasquerades(t *testing.T) {
	requireRoot(t)
	ctrPid := startNetnsHelper(t)
	extPid := startNetnsHelper(t)
	cfg := Config{
		HostVeth: "veth-t7", CtrVeth: "ceth-t7",
		BridgeName: "br-t7", BridgeCIDR: "10.220.6.1/24", ContainerCIDR: "10.220.6.2/24",
		HostPort: 17700, ContainerPort: 7700,
	}
	// 「外部主机」：宿主侧 10.221.0.1/24、外部侧 10.221.0.2/24，直连 veth，不接网桥、另一个网段。
	ext := Config{HostVeth: "veth-x7", CtrVeth: "ceth-x7", ContainerCIDR: "10.221.0.2/24"}

	if err := AttachVeth(ctrPid, cfg); err != nil {
		t.Fatalf("AttachVeth: %v", err)
	}
	if err := ConnectBridge(ctrPid, cfg); err != nil {
		t.Fatalf("ConnectBridge: %v", err)
	}
	if err := AttachVeth(extPid, ext); err != nil {
		t.Fatalf("AttachVeth ext: %v", err)
	}
	for _, args := range [][]string{
		{"addr", "add", "10.221.0.1/24", "dev", ext.HostVeth},
		{"link", "set", ext.HostVeth, "up"},
	} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v (%s)", args, err, out)
		}
	}
	t.Cleanup(func() {
		_ = exec.Command("ip", "link", "del", cfg.HostVeth).Run()
		_ = exec.Command("ip", "link", "del", ext.HostVeth).Run()
		_ = exec.Command("ip", "link", "del", cfg.BridgeName).Run()
	})

	logPath := startListener(t, extPid, 7700, "-v", "-n")
	if err := EnablePublish(cfg); err != nil {
		t.Fatalf("EnablePublish: %v", err)
	}
	t.Cleanup(func() { _ = Teardown(cfg) })

	send := exec.Command("nsenter", "--net=/proc/"+strconv.Itoa(ctrPid)+"/ns/net", "--",
		"timeout", "1", "bash", "-c", "echo m04-egress-test | nc 10.221.0.2 7700")
	_ = send.Run()
	time.Sleep(300 * time.Millisecond)

	got, _ := os.ReadFile(logPath)
	if !strings.Contains(string(got), "m04-egress-test") {
		t.Fatalf("容器应该能访问到外部主机 10.221.0.2:7700（漏了 saddr masquerade 时回包无路可走，连接建不起来），listener log: %q", got)
	}
	if !strings.Contains(string(got), "10.221.0.1") {
		t.Fatalf("外部主机看到的源地址应该已被 masquerade 换成宿主出口地址 10.221.0.1，而不是容器私网地址，listener log: %q", got)
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

// TestM04P4TeardownWithoutPublish 盯住没带 -p 的容器：它从来没建过 nat 表，Teardown 里那条
// nft delete table 注定失败——如果照抄"出错就 return"的写法，veth 就永远删不掉，宿主上会
// 一次一条地攒下死网卡。
func TestM04P4TeardownWithoutPublish(t *testing.T) {
	requireRoot(t)
	pid := startNetnsHelper(t)
	cfg := Config{
		HostVeth: "veth-t9", CtrVeth: "ceth-t9",
		BridgeName: "br-t9", BridgeCIDR: "10.220.8.1/24", ContainerCIDR: "10.220.8.2/24",
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

	// 注意：这里故意不调用 EnablePublish——模拟 `mini-docker run --net`（不带 -p）的容器。
	if err := Teardown(cfg); err != nil {
		t.Fatalf("没发布端口的容器 Teardown 不该失败（那张 nat 表本来就不存在）: %v", err)
	}
	if err := exec.Command("ip", "link", "show", cfg.HostVeth).Run(); err == nil {
		t.Fatalf("HostVeth %s 必须被删掉，即使前面没有 nat 表可删", cfg.HostVeth)
	}
}
