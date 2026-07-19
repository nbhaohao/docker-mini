#!/bin/sh
# m02-p1 观察关：cgroup v2 不是什么魔法接口，就是 /sys/fs/cgroup 下一棵普通目录树,
# 建目录=建资源组、目录里的文件=可读可写的控制接口。
# VM 里跑：orb -m backend sudo sh scripts/checks-m02-p1.sh
set -e

mount | grep -q 'on /sys/fs/cgroup type cgroup2' \
  && echo "✅ cgroup2 是一个真实挂载点：mount 能看到它,和 ext4/overlay 一样是个文件系统"

[ -f /sys/fs/cgroup/cgroup.controllers ] \
  && echo "✅ cgroup.controllers 是普通文件：cat 它就能看到内核支持哪些控制器 -> $(cat /sys/fs/cgroup/cgroup.controllers)"

mkdir -p /sys/fs/cgroup/mini-docker
echo "+memory +cpu +pids" > /sys/fs/cgroup/mini-docker/cgroup.subtree_control
mkdir -p /sys/fs/cgroup/mini-docker/observe-demo
echo "✅ mkdir 一个目录 = 创建一个资源组：/sys/fs/cgroup/mini-docker/observe-demo 一出现就自带 memory.max/cpu.max/pids.max 这些接口文件"

sh -c 'echo $$ > /sys/fs/cgroup/mini-docker/observe-demo/cgroup.procs; cat /sys/fs/cgroup/mini-docker/observe-demo/cgroup.procs'
echo "✅ 把 pid 写进 cgroup.procs = 把那个进程加入这个组；cat 同一个文件能读出当前组里有哪些 pid"

rmdir /sys/fs/cgroup/mini-docker/observe-demo
echo "✅ rmdir 删组：组内没有存活进程时,普通目录删除操作就能销毁一个资源组"
