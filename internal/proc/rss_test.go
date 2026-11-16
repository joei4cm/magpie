package proc

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// A running tree's resident memory is read, and none once it was waited
// for: its pid may be another process's by then.
func TestTreeRSS(t *testing.T) {
	name, args := "sleep", []string{"30"}
	if runtime.GOOS == "windows" {
		name, args = "ping", []string{"-n", "30", "127.0.0.1"}
	}
	if _, err := exec.LookPath(name); err != nil {
		t.Skip("no " + name)
	}
	tree, err := StartTree(exec.Command(name, args...))
	if err != nil {
		t.Fatal(err)
	}
	// just after its exec a command holds a few pages, until its loader
	// maps what it runs on (16KiB on ubuntu CI), so it is read until it
	// does; under WSL2 the first read can say 0 kB, which isn't a reading
	// (#4033)
	rss, ok := tree.RSS()
	for end := time.Now().Add(10 * time.Second); (!ok || rss < 64<<10) && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
		rss, ok = tree.RSS()
	}
	if !ok || rss < 64<<10 || rss > 1<<30 {
		t.Fatalf("a running %s holds %d bytes (%v)", name, rss, ok)
	}
	tree.Kill()
	_ = tree.Wait()
	if _, ok := tree.RSS(); ok {
		t.Fatal("a tree waited for was read")
	}
	if MemTotal() <= 0 && (runtime.GOOS == "darwin" || runtime.GOOS == "linux" || runtime.GOOS == "windows") {
		t.Fatal("this computer's memory isn't known")
	}
}

// A tree's resident memory is the memory of every process of it: a
// process the command started is counted with it, and what it holds is
// what ending the tree frees. A Claude Code run's tree is magpie's own
// helper with Claude Code under it, in a session of its own, and the run
// was read as the helper's alone (#6599).
func TestTreeRSSCountsWhatItStarted(t *testing.T) {
	const held = 128 << 20
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "MAGPIE_PROC_TREE=128")
	tree, err := StartTree(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		tree.Kill()
		_ = tree.Wait()
	}()
	var rss int64
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); {
		time.Sleep(100 * time.Millisecond)
		v, ok := tree.RSS()
		if !ok {
			continue
		}
		rss = v
		if rss >= held {
			break
		}
	}
	if rss < held {
		t.Fatalf("the tree holds %d bytes, want the %d its own process holds", rss, held)
	}
}
