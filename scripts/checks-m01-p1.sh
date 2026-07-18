#!/bin/sh
# m01-p1 观察关：用 strace 亲眼看 echo hi 底层的系统调用。
# VM 里跑：orb -m backend sh scripts/checks-m01-p1.sh
set -e

out=$(strace -f -e trace=clone,execve,write echo hi 2>&1)

echo "$out" | grep -q 'execve(' \
  && echo "✅ execve：当前进程内存镜像被整个换成 /usr/bin/echo（pid 不变）"
echo "$out" | grep -q 'write(1, "hi' \
  && printf '%s\n' '✅ write(1, "hi\n", 3)：用户态程序只能通过系统调用把字节送到终端'
echo "$out" | grep -q '+++ exited with 0 +++' \
  && echo "✅ 进程正常退出：一条 echo 的完整一生 = execve 换像 → write 输出 → exit"
