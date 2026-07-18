// 已就位（AI 生成）：run/child 重新执行调度 + 参数解析，纯样板，不含 namespace 机制本身。
package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/nbhaohao/docker-mini/isolate"
)

func parseFlags(args []string) (isolate.NamespaceFlags, []string) {
	var f isolate.NamespaceFlags
	i := 0
	for ; i < len(args); i++ {
		switch args[i] {
		case "--uts":
			f.UTS = true
		case "--pid":
			f.PID = true
		case "--ns":
			f.NS = true
		case "--":
			i++
			return f, args[i:]
		default:
			return f, args[i:]
		}
	}
	return f, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mini-docker run|child [--uts] [--pid] [--ns] -- <cmd> [args...]")
		os.Exit(2)
	}
	flags, rest := parseFlags(os.Args[2:])
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "missing target command after --")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		runMode(flags, rest)
	case "child":
		childMode(flags, rest)
	default:
		fmt.Fprintln(os.Stderr, "unknown mode:", os.Args[1])
		os.Exit(2)
	}
}

func runMode(f isolate.NamespaceFlags, target []string) {
	self, err := os.Executable()
	must(err)
	childArgs := append([]string{"child"}, flagArgs(f)...)
	childArgs = append(childArgs, "--")
	childArgs = append(childArgs, target...)
	cmd := exec.Command(self, childArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = isolate.SysProcAttrFor(f)
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
}

func childMode(f isolate.NamespaceFlags, target []string) {
	if err := isolate.EnterAndExec(f, target); err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(1)
	}
}

func flagArgs(f isolate.NamespaceFlags) []string {
	var out []string
	if f.UTS {
		out = append(out, "--uts")
	}
	if f.PID {
		out = append(out, "--pid")
	}
	if f.NS {
		out = append(out, "--ns")
	}
	return out
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
