package network

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Config 描述一个容器的网络身份：网桥两侧各叫什么名字、分到哪个网段的哪个地址，
// 以及（可选的）宿主端口发布映射。四个 phase 共用同一份 Config，字段随 phase 逐步用起来。
type Config struct {
	HostVeth      string // 宿主侧网卡名，例如 "veth-c1"
	CtrVeth       string // 容器侧网卡名，例如 "ceth-c1"
	BridgeName    string // 网桥名，例如 "mini-docker0"
	BridgeCIDR    string // 网桥自己的地址，例如 "10.200.0.1/24"
	ContainerCIDR string // 容器侧网卡的地址，例如 "10.200.0.2/24"
	HostPort      int    // 宿主要发布的端口，0 = 不发布
	ContainerPort int    // 容器进程实际监听的端口
}

// gatewayIP 从 BridgeCIDR（"10.200.0.1/24"）里摘出网桥自己的 IP（"10.200.0.1"），
// 这就是容器侧要走的默认网关地址——纯字符串切分，不涉及网络操作，直接可用。
func gatewayIP(cidr string) string {
	for i := 0; i < len(cidr); i++ {
		if cidr[i] == '/' {
			return cidr[:i]
		}
	}
	return cidr
}

// runIP 是本包对 `ip` 命令的统一封装：本课不手写 netlink 协议，允许用 exec.Command
// 编排系统原语（COURSE_SPEC m04 实现边界明确允许）。出错时把完整命令行带进 error，
// 方便直接在 VM 里复制这条命令单独重跑排查。
func runIP(args ...string) error {
	if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ip %v: %w (%s)", args, err, out)
	}
	return nil
}

// runInNetns 把 args 描述的命令放进 pid 这个进程当前所在的 network namespace 里执行。
// namespace 归属跟着 pid 走、不跟着"当前在跑哪个可执行文件"走——这正是本课能省掉宿主/子
// 进程握手同步的关键：不管 pid 这个进程此刻是还在跑 mini-docker 自己的代码，还是已经
// execve 换像成了目标命令，/proc/<pid>/ns/net 指向的都是同一个 namespace，nsenter 随时能进。
func runInNetns(pid int, args ...string) error {
	full := append([]string{"--net=/proc/" + strconv.Itoa(pid) + "/ns/net", "--"}, args...)
	if out, err := exec.Command("nsenter", full...).CombinedOutput(); err != nil {
		return fmt.Errorf("nsenter %v: %w (%s)", full, err, out)
	}
	return nil
}

// bridgeExists 回答"这张网桥是不是已经在了"。网桥是所有容器共用的公共资源（作用等同 docker0）：
// 第一个容器负责把它建出来，后面的容器只是把自己的 veth 接上去——对已存在的网桥再执行一次
// ip link add 会直接报 RTNETLINK answers: File exists，所以建之前必须先问一句。
func bridgeExists(name string) bool {
	return exec.Command("ip", "link", "show", name).Run() == nil
}

// natTableName 派生这个容器专属的 nftables 表名，EnablePublish 建它、Teardown 删它，
// 两处共用同一个名字，保证"一条命令连带删掉全部规则"这条 cleanup 承诺不会走样。
func natTableName(cfg Config) string {
	return "mini_docker_nat_" + cfg.CtrVeth
}

// AttachVeth 是 m04 P2 的核心：在宿主建一对 veth，把 CtrVeth 那一端精确移入 pid 对应的
// network namespace，再在里面配好 IP 并拉起来（新 namespace 里所有网卡默认都是 DOWN 的）。
// HostVeth 这一端这里只创建、不配置——它要接进网桥，那是 P3 的事。
func AttachVeth(pid int, cfg Config) error {
	if err := runIP("link", "add", cfg.HostVeth, "type", "veth", "peer", "name", cfg.CtrVeth); err != nil {
		return err
	}
	if err := runIP("link", "set", cfg.CtrVeth, "netns", strconv.Itoa(pid)); err != nil {
		return err
	}
	if err := runInNetns(pid, "ip", "addr", "add", cfg.ContainerCIDR, "dev", cfg.CtrVeth); err != nil {
		return err
	}
	return runInNetns(pid, "ip", "link", "set", cfg.CtrVeth, "up")
}

// 你来实现（m04 P2）：
// 1. runIP("link", "add", cfg.HostVeth, "type", "veth", "peer", "name", cfg.CtrVeth)
//    —— 一条命令建出一对"背靠背"的虚拟网卡，两端此刻都还在宿主自己的 netns 里
// 2. runIP("link", "set", cfg.CtrVeth, "netns", strconv.Itoa(pid))
//    —— 把 CtrVeth 这一端移交给 pid 所在的 network namespace（HostVeth 留在宿主不动）
// 3. runInNetns(pid, "ip", "addr", "add", cfg.ContainerCIDR, "dev", cfg.CtrVeth)
//    —— 到 pid 的 namespace 里给刚搬进去的网卡配上容器侧地址
// 4. runInNetns(pid, "ip", "link", "set", cfg.CtrVeth, "up")
//    —— 光配地址不够，新搬进来的网卡默认是 DOWN 的，必须显式拉起来
// 5. 任何一步出错就直接返回该 error；全部成功返回 nil

// ConnectBridge 是 m04 P3 的核心：建好网桥、把 P2 留在宿主侧的 HostVeth 接成网桥的一个端口，
// 再让容器侧把默认路由指向网桥地址——做完这一步，容器与宿主才算真正"打通"（能互相 ping 通）。
func ConnectBridge(pid int, cfg Config) error {
	panic("TODO: m04 P3 - 建网桥、把宿主侧网卡接进网桥、给容器配默认路由")
}

// 你来实现（m04 P3）：
// 1~3. 只有 !bridgeExists(cfg.BridgeName) 时才建网桥并配地址、拉起来：
//    runIP("link", "add", cfg.BridgeName, "type", "bridge")   —— 网桥本质也是一种网卡类型
//    runIP("addr", "add", cfg.BridgeCIDR, "dev", cfg.BridgeName)  —— 容器的默认网关就是这个地址
//    runIP("link", "set", cfg.BridgeName, "up")
//    网桥是所有容器共用的：第二个容器再来一次 ip link add 会直接失败（File exists），
//    所以这三步必须包在"不存在才做"里，ConnectBridge 才是可以被反复调用的（幂等）。
// 4. runIP("link", "set", cfg.HostVeth, "master", cfg.BridgeName)
//    —— "master" 是把一张网卡接进网桥当端口的标准写法；接完它只做二层转发，不再需要自己的 IP
// 5. runIP("link", "set", cfg.HostVeth, "up")
// 6. runInNetns(pid, "ip", "route", "add", "default", "via", gatewayIP(cfg.BridgeCIDR))
//    —— 容器侧此后所有"不知道往哪走"的包，都送去网桥这个网关
// 7. 任何一步出错就直接返回该 error；全部成功返回 nil

// subnetOf 把 "10.200.0.1/24" 这样的地址换成它所在的子网 "10.200.0.0/24"——nftables 的
// masquerade 规则按「容器子网」匹配，而不是按某一个具体地址匹配。纯字符串处理，直接可用。
func subnetOf(cidr string) string {
	ip := gatewayIP(cidr)
	mask := cidr[len(ip):]
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return cidr
	}
	return parts[0] + "." + parts[1] + "." + parts[2] + ".0" + mask
}

// EnablePublish 是 m04 P4 的核心：打开转发 + 装一张只属于这个容器的 nftables nat 表。
// 四条规则缺一都会漏——prerouting/output 各一条 DNAT，分别接住"外部发来的包"和"宿主自己
// 发起的包"两种不同入口；postrouting 两条 masquerade，一条按源地址覆盖"容器访问外网"，
// 另一条按目的地址覆盖"宿主用 127.0.0.1:HostPort 回环访问自己发布的端口"这个隐蔽的
// hairpin 场景：DNAT 只换了目的地址，容器看到的还是宿主的真实源地址 127.0.0.1，容器的
// 回包会直接往 127.0.0.1 送而不是经过网桥，宿主自己都不知道该怎么把这个回包收回来——
// 必须连源地址也换成网桥地址，回包才有路可走（这条踩坑记录见 P4 教案）。
func EnablePublish(cfg Config) error {
	panic("TODO: m04 P4 S1 - 开转发 + 建 nat 表四条规则")
}

// 你来实现（m04 P4 S1）：
// 1. 打开转发：exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").Run()
//    —— 内核默认只收发「发给自己的包」，要帮容器把包转出去必须显式打开这一项
// 2. 只对网桥打开 route_localnet：
//    exec.Command("sysctl", "-w", "net.ipv4.conf."+cfg.BridgeName+".route_localnet=1").Run()
//    —— 内核默认把 127.0.0.0/8 当作「只能待在本机内部」的地址，DNAT 之后这个包要被路由
//    出网桥，默认规则会直接把它丢掉；route_localnet 就是对这张网卡解除这条限制。
//    这个开关是按网卡生效的：网桥被删掉时它跟着一起消失，不会在宿主上留下全局残留
//    （所以不要去动 lo 或 all，更不要为了「保险」关掉 rp_filter——那是在削弱宿主的安全策略）。
// 3. table := natTableName(cfg)；用 exec.Command("nft", ...) 依次：
//    nft add table ip <table>
//    nft add chain ip <table> prerouting  '{ type nat hook prerouting  priority -100 ; }'
//    nft add chain ip <table> output      '{ type nat hook output      priority -100 ; }'
//    nft add chain ip <table> postrouting '{ type nat hook postrouting priority  100 ; }'
//    nft add rule  ip <table> prerouting  tcp dport <HostPort> dnat to <容器IP>:<ContainerPort>
//    nft add rule  ip <table> output      tcp dport <HostPort> dnat to <容器IP>:<ContainerPort>
//    nft add rule  ip <table> postrouting ip saddr <容器子网> masquerade
//    nft add rule  ip <table> postrouting ip daddr <容器子网> masquerade   ← 就是上面注释里那条 hairpin 规则
//    （<容器子网> 用 subnetOf(cfg.BridgeCIDR)，<容器IP> 用 gatewayIP(cfg.ContainerCIDR)）
// 4. 任何一步出错就直接返回该 error；全部成功返回 nil

// Teardown 撤销 AttachVeth/ConnectBridge/EnablePublish 建立的一切属于这个容器的东西：
// 整表删掉这个容器专属的 nat 表（连带删掉表里全部四条规则），再删宿主侧的 HostVeth——
// 内核维护的是同一对 veth，删掉宿主这一半，容器里那一半会跟着自动消失，不需要分别处理。
// 网桥本身不在这里删：它是可能被多个容器共用的公共资源。
func Teardown(cfg Config) error {
	panic("TODO: m04 P4 S2 - 删这个容器专属的 nat 表 + 删 veth")
}

// 你来实现（m04 P4 S2）：
// 1. exec.Command("nft", "delete", "table", "ip", natTableName(cfg)).Run()
//    —— 这一步的错误要**忽略**：没带 -p 发布端口的容器压根没建过这张表，删不掉是正常的。
//    如果照抄前面几个函数"出错就 return"的写法，veth 就永远删不掉了——残留的正是这么来的。
// 2. runIP("link", "del", cfg.HostVeth)  —— 这一步的错误才需要返回
// 3. 网桥不删：它是所有容器共用的公共资源（Docker 也不会在容器退出时删掉 docker0）
