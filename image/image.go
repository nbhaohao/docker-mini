package image

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// MountOverlay 把 lower(只读镜像层) + upper(容器可写层) 联合挂载到 merged 这个目录上，
// work 是 overlayfs 要求的暂存目录（内核用它做原子重命名，不能被外部直接读写）。
func MountOverlay(lower, upper, work, merged string) error {
	data := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, upper, work)
	return syscall.Mount("overlay", merged, "overlay", 0, data)
}

// 你来实现（P2）：
// 把 data 拼成 "lowerdir=<lower>,upperdir=<upper>,workdir=<work>" 这个字符串，
// 调用 syscall.Mount("overlay", merged, "overlay", 0, data) 并返回它的 error

// PivotInto 把当前进程的根文件系统切换到 newRoot（必须已经是一个挂载点，比如 MountOverlay 的 merged）。
// 切换完成后旧的根会被挂到 newRoot/.old_root 再原地卸载丢弃，调用者之后看到的 "/" 就是 newRoot。
//
// P2 完成前这个函数只是占位：先跑通 MountOverlay，再来替换下面这行 panic。
func PivotInto(newRoot string) error {
	if err := syscall.Mount(newRoot, newRoot, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return err
	}
	oldRoot := filepath.Join(newRoot, ".old_root")
	if err := os.MkdirAll(oldRoot, 0700); err != nil {
		return err
	}
	if err := syscall.PivotRoot(newRoot, oldRoot); err != nil {
		return err
	}
	if err := os.Chdir("/"); err != nil {
		return err
	}
	if err := syscall.Unmount("/.old_root", syscall.MNT_DETACH); err != nil {
		return err
	}
	return os.RemoveAll("/.old_root")
}

// 你来实现（P3，替换上面那行 panic）：
// 1. syscall.Mount(newRoot, newRoot, "", syscall.MS_BIND|syscall.MS_REC, "") —— 把 newRoot 绑到自己身上，
//    这是 pivot_root(2) 的硬性要求：newRoot 必须是一个挂载点，不能只是普通目录
// 2. oldRoot := filepath.Join(newRoot, ".old_root")；os.MkdirAll(oldRoot, 0700) 创建旧根的落脚点
// 3. syscall.PivotRoot(newRoot, oldRoot) —— 内核在这一步把当前的根换成 newRoot，旧根被挂到 oldRoot 下
// 4. os.Chdir("/") —— pivot_root 不会自动更新当前工作目录，必须手动切回新根的 "/"
// 5. syscall.Unmount("/.old_root", syscall.MNT_DETACH) 卸载旧根，再 os.RemoveAll("/.old_root") 清理落脚点
// 6. 返回过程中遇到的第一个 error（或 nil）

// PrepareContainerRoot 在 runtimeDir 下为一次运行建好 upper/work/merged 三个子目录，
// 并调用 MountOverlay 把 lower（镜像层）和这三层联合挂载到 merged，返回 merged 路径给调用者传给 PivotInto。
//
// P3 完成前这个函数只是占位：先跑通 PivotInto，再来替换下面这行 panic。
func PrepareContainerRoot(runtimeDir, lower string) (merged string, err error) {
	upper := filepath.Join(runtimeDir, "upper")
	work := filepath.Join(runtimeDir, "work")
	merged = filepath.Join(runtimeDir, "merged")
	for _, dir := range []string{upper, work, merged} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}
	}
	if err := MountOverlay(lower, upper, work, merged); err != nil {
		return "", err
	}
	return merged, nil
}

// 你来实现（P4，替换上面那行 panic）：
// 1. upper := filepath.Join(runtimeDir, "upper")；work := filepath.Join(runtimeDir, "work")；
//    merged = filepath.Join(runtimeDir, "merged")
// 2. 对 upper/work/merged 三个目录分别 os.MkdirAll(d, 0755)，任何一步出错就返回 "", err
// 3. 调用 MountOverlay(lower, upper, work, merged)，出错返回 "", err
// 4. 返回 merged, nil
