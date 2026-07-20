#!/bin/sh
# m03-p1 观察关：一个"镜像"到底是什么——下载 alpine 官方 mini-rootfs，亲眼看它就是一份普通的目录树 tar 包，
# 没有任何魔法。这份 rootfs 会缓存进 .cache/alpine-rootfs/，P4 收官关直接复用，不重复下载。
# VM 里跑：orb -m backend sh scripts/checks-m03-p1.sh
set -e

CACHE_DIR="$(cd "$(dirname "$0")/.." && pwd)/.cache/alpine-rootfs"
URL="https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/aarch64/alpine-minirootfs-3.24.1-aarch64.tar.gz"

if [ ! -f "$CACHE_DIR/bin/sh" ]; then
  mkdir -p "$CACHE_DIR"
  curl -sL -o /tmp/alpine-minirootfs.tar.gz "$URL"
  tar -xzf /tmp/alpine-minirootfs.tar.gz -C "$CACHE_DIR"
  rm -f /tmp/alpine-minirootfs.tar.gz
fi

[ -x "$CACHE_DIR/bin/busybox" ] \
  && echo "✅ 解压后 bin/busybox 是一个真实可执行文件：镜像的本质就是一份目录树 + 里面的二进制，没有虚拟磁盘、没有 Hypervisor（bin/sh 只是指向它的符号链接，指向绝对路径 /bin/busybox——这条链接现在从宿主视角是断的，只有真正 pivot_root 进这份 rootfs 之后它才会解析到正确的地方）"

grep -q 'Alpine Linux' "$CACHE_DIR/etc/os-release" \
  && echo "✅ etc/os-release 里写着 Alpine Linux：这份 rootfs 和宿主（Ubuntu）完全是两套发行版的文件，只是还没被当成谁的根目录"

[ ! -e "$CACHE_DIR/proc/1" ] \
  && echo "✅ $CACHE_DIR/proc 是空的：这份 rootfs 只是静态文件，/proc 这类运行时信息要等它真正成为某个进程的根、且被 mount 之后才会出现"
