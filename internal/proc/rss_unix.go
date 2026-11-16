//go:build !windows

package proc

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// rss reads the resident memory of the command's tree with t.mu let go
// first: on macOS it asks ps, which Wait and Kill don't wait behind.
func (t *Tree) rss(cmd *exec.Cmd) (int64, bool) {
	pid := cmd.Process.Pid
	t.mu.Unlock()
	return treeRSS(pid)
}

// treeRSS is the resident memory of pid and of every process it started,
// and of theirs, in bytes: what a run holds and what ending it frees. The
// command alone was what it read before, and for a Claude Code run that
// was magpie's own helper: Claude Code ran under it, in a session of its
// own, and the run's memory was read as the helper's (#6599).
func treeRSS(pid int) (int64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := CommandContext(ctx, "ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "rss=").Output()
	if err != nil {
		return pidRSS(pid)
	}
	kids, rss := parseTreeRSS(string(out))
	if _, ok := rss[pid]; !ok {
		return pidRSS(pid)
	}
	total := rss[pid]
	seen := map[int]bool{pid: true}
	for queue := []int{pid}; len(queue) > 0; queue = queue[1:] {
		for _, k := range kids[queue[0]] {
			if seen[k] {
				continue
			}
			seen[k] = true
			total += rss[k]
			queue = append(queue, k)
		}
	}
	return total, true
}

// parseTreeRSS reads lines of "pid ppid resident memory in KiB", as ps
// prints them: every process' parent, and what each holds.
func parseTreeRSS(out string) (map[int][]int, map[int]int64) {
	kids := map[int][]int{}
	rss := map[int]int64{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		id, err1 := strconv.Atoi(f[0])
		parent, err2 := strconv.Atoi(f[1])
		kb, err3 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || id <= 0 {
			continue
		}
		kids[parent] = append(kids[parent], id)
		rss[id] = kb << 10
	}
	return kids, rss
}

// pidRSS is pid's resident memory: Linux's /proc/<pid>/status VmRSS, ps
// elsewhere. Both say it in KiB.
func pidRSS(pid int) (int64, bool) {
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "VmRSS:" {
				return parseKiB(f[1])
			}
		}
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	return parseKiB(strings.TrimSpace(string(out)))
}

func parseKiB(s string) (int64, bool) {
	kb, err := strconv.ParseInt(s, 10, 64)
	if err != nil || kb <= 0 {
		return 0, false
	}
	return kb << 10, true
}
