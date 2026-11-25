//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// hogus2037 on macOS (#7750): a vault registered in obsidian.json that
// sits on a File Provider volume whose provider stopped answering — a
// vault under ~/Library/CloudStorage whose OneDrive sync stalled — never
// answers a read, and the start-up scan held the app for ever in it: the
// process ran, but no window and no log line ever came. The vault's own
// folder is known, so the scan listed it; Copilot's settings in it are
// what hung. A FIFO stands in for such a volume here: stat succeeds, and
// the read waits for a writer that never comes.
func TestObsidianStalledVaultDoesNotStopTheScan(t *testing.T) {
	home := syncHome(t)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, ".config")
	odir := obsidianDirs(home, cfg)[0]
	notes := filepath.Join(home, "Documents", "Notes")
	obsidianVault(t, odir, "a1b2c3d4e5f60718", notes, "4.0.19", obsCopilotUser)
	stalled := filepath.Join(home, "Library", "CloudStorage", "OneDrive-acct", "Vault")
	data := obsidianVault(t, odir, "b2c3d4e5f6071829", stalled, "4.0.19", obsCopilotUser)
	// the volume stopped answering: its folder is there, its files never
	// come back
	os.Remove(data)
	if err := syscall.Mkfifo(data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// let each look the scan left behind in the FIFO finish: a writer
		// opens only while a reader waits in its own open, and a regular
		// file would take the open every time
		if fi, err := os.Lstat(data); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
			return
		}
		for i := 0; i < 8; i++ {
			fd, err := syscall.Open(data, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				return
			}
			syscall.Close(fd)
		}
	})

	// the vault's folder is known, so it is still listed
	if got := obsidianVaults(obsidianDirs(home, cfg)); len(got) != 2 {
		t.Fatalf("vaults %v", got)
	}
	a := obsidian(home, cfg)
	if !a.Detected() {
		t.Fatal("not found with a vault that has Copilot")
	}
	// the read the start-up scan makes of every vault's Copilot settings
	// is what hung: it is answered, with the stalled vault not wired
	f := a.Field("provider")
	got := make(chan string, 1)
	go func() { got <- f.Get() }()
	select {
	case v := <-got:
		if v != "" {
			t.Fatalf("the stalled vault was read as wired: %q", v)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the start-up scan waited on a vault that never answers")
	}
	// setting the provider names the vault it couldn't read, and the set is
	// put back whole: one vault magpie can't read leaves the others as
	// they were rather than half of them wired (edit.Atomically)
	set := make(chan error, 1)
	go func() { set <- f.Set(magpieID) }()
	select {
	case err := <-set:
		if err == nil || !strings.Contains(err.Error(), data) {
			t.Fatalf("the stalled vault was not named: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("setting the provider waited on a vault that never answers")
	}
	wired, _ := obsReadCopilot(t, obsCopilotData(notes))
	if _, ok := wired.Providers[magpieID]; ok {
		t.Fatalf("magpie's provider was written over a failed set: %s", obsCopilotData(notes))
	}
	if fi, err := os.Lstat(data); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("the stalled vault's settings were written: %v", err)
	}
}

// A vault whose folder never answers is one with no Copilot in it either:
// the scan lists what answers and goes on. obsDirAt is the look a test
// holds back, as wslRoot's stat is.
func TestObsidianStalledVaultFolderSkipped(t *testing.T) {
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	wait, dirAt := obsProbeWait, obsDirAt
	t.Cleanup(func() { obsProbeWait, obsDirAt = wait, dirAt })
	obsProbeWait = 100 * time.Millisecond

	home := syncHome(t)
	cfg := filepath.Join(home, ".config")
	odir := obsidianDirs(home, cfg)[0]
	notes := filepath.Join(home, "Documents", "Notes")
	obsidianVault(t, odir, "a1b2c3d4e5f60718", notes, "4.0.19", obsCopilotUser)
	stalled := filepath.Join(home, "Library", "CloudStorage", "OneDrive-acct", "Vault")
	obsidianVault(t, odir, "b2c3d4e5f6071829", stalled, "4.0.19", obsCopilotUser)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	obsDirAt = func(p string) bool {
		if p == stalled {
			<-release
		}
		return dirAt(p)
	}

	start := time.Now()
	if got := obsidianVaults(obsidianDirs(home, cfg)); len(got) != 1 || got[0] != notes {
		t.Fatalf("vaults %v, want only %s", got, notes)
	}
	a := obsidian(home, cfg)
	if !a.Detected() {
		t.Fatal("the vault that answers was lost with the one that doesn't")
	}
	if v := a.Field("provider").Get(); v != "" {
		t.Fatalf("the stalled vault was read as wired: %q", v)
	}
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatalf("setting the provider waited on the stalled vault: %v", err)
	}
	wired, _ := obsReadCopilot(t, obsCopilotData(notes))
	if _, ok := wired.Providers[magpieID]; !ok {
		t.Fatalf("magpie's provider was not written: %s", obsCopilotData(notes))
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("the scan took %v", d)
	}
}
