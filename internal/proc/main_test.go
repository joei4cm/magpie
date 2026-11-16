package proc

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// The tests run in a home of their own, never the user's real files.
// With MAGPIE_PROC_RAN set, the test binary stands in for a program a
// probe asks its version (guiexe_windows_test.go): it notes that it was
// started there and answers.
//
// With MAGPIE_PROC_SLEEP set, it runs until its stdin closes
// (listener_test.go).
//
// With MAGPIE_PROC_LISTEN set to an address, it listens there first, says
// "listening" and then runs the same way (holder_test.go).
//
// With MAGPIE_PROC_TREE set to a number of MiB, it stands in for a
// command that starts a process of its own: it starts one holding that
// much memory and waits for it (rss_test.go). With MAGPIE_PROC_HOLD set
// to a number of MiB, it holds that much memory and waits.
func TestMain(m *testing.M) {
	if addr := os.Getenv("MAGPIE_PROC_LISTEN"); addr != "" {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		defer ln.Close()
		fmt.Println("listening")
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	if os.Getenv("MAGPIE_PROC_SLEEP") != "" {
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	if hold := os.Getenv("MAGPIE_PROC_HOLD"); hold != "" {
		holdMemory(hold)
		os.Exit(0)
	}
	if tree := os.Getenv("MAGPIE_PROC_TREE"); tree != "" {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "MAGPIE_PROC_HOLD="+tree)
		if err := cmd.Start(); err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		_ = cmd.Wait()
		os.Exit(0)
	}
	if ran := os.Getenv("MAGPIE_PROC_RAN"); ran != "" {
		if f, err := os.OpenFile(ran, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, os.Args[0])
			f.Close()
		}
		fmt.Println("2.2.5")
		os.Exit(0)
	}
	testenv.Main(m)
}

// holdMemory holds the MiB the env var names, resident, and waits: the
// memory a page only counts once it is written to.
func holdMemory(mib string) {
	n, err := strconv.Atoi(mib)
	if err != nil || n <= 0 {
		return
	}
	mem := make([]byte, n<<20)
	for i := range mem {
		mem[i] = byte(i)
	}
	time.Sleep(60 * time.Second)
}
