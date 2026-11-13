package plugin

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// heavyFor sets the host's memory bound for one test (heavyMB and its
// timing), looked at every 20ms from the host's start, and lets a host be
// replaced for holding too much every 20ms as well. The growth a burst
// needs (heavyGrow) is put out of reach: a test that wants one sets it.
func heavyFor(t *testing.T, mb int) {
	t.Helper()
	wasMB, wasFast, wasEvery, wasGrow := heavyMB, heavyFast, heavyEvery, heavyGrow
	heavyMB, heavyFast, heavyEvery, heavyGrow = mb, 20*time.Millisecond, 20*time.Millisecond, 1<<30
	t.Cleanup(func() { heavyMB, heavyFast, heavyEvery, heavyGrow = wasMB, wasFast, wasEvery, wasGrow })
}

func currentHost() *host {
	hostMu.Lock()
	defer hostMu.Unlock()
	return current
}

// A host holding more than heavyMB after a full collection is replaced:
// it ends, and the next call is answered by a new host. A host that grew
// with what its plugins kept was otherwise left to grow until the system
// killed it (783 MiB after 2h25m, OOM-killed on a small Linux server).
// Any host holds more than 1 MiB.
func TestHeavyHostIsReplaced(t *testing.T) {
	heavyFor(t, 1) // restored after sandbox's Settle has ended the hosts
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	was := recycles.Load()
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(30 * time.Second)
	for recycles.Load() == was {
		if time.Now().After(until) {
			t.Fatal("a host past its memory bound was not replaced 30s on")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// the one replaced ends, and a new host answers (replaced in its turn,
	// past the bound too)
	until = time.Now().Add(30 * time.Second)
	for {
		hostMu.Lock()
		n := len(retiring)
		hostMu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("the replaced host was still finishing its calls 30s on")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := Call(ctx, "providers", map[string]any{}, nil); err != nil {
		t.Fatalf("a call after the heavy host was replaced: %v", err)
	}
}

// A host under the bound is left alone, however often it checks.
func TestLightHostIsKept(t *testing.T) {
	heavyFor(t, 1<<20)
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	was := recycles.Load()
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	h := currentHost()
	if h == nil {
		t.Fatal("no host")
	}
	// fifty checks
	time.Sleep(time.Second)
	if recycles.Load() != was || currentHost() != h || !h.alive() {
		t.Fatal("a host under its memory bound was replaced")
	}
}

// A host that grew fast is replaced without waiting for its turn: a box
// with little room left has none to spare (#6458: 25 to 30 MB every 50 ms
// in the report behind it). A host that grew slowly waits for its turn, so
// a plugin that holds that much for every call costs a host a minute and
// not a second.
func TestHeavyBurstIsNotWaitedFor(t *testing.T) {
	wasEvery, wasGrow, wasLast := heavyEvery, heavyGrow, heavyLast
	heavyEvery, heavyGrow = time.Minute, 128
	heavyLast = time.Time{}
	t.Cleanup(func() { heavyEvery, heavyGrow, heavyLast = wasEvery, wasGrow, wasLast })
	if !heavyAllowed(0) {
		t.Fatal("the first host over the bound was not replaced")
	}
	if heavyAllowed(0) {
		t.Fatal("a second host over the bound was replaced without waiting for its turn")
	}
	if !heavyAllowed(128) {
		t.Fatal("a host that grew fast was waited for")
	}
}

// A host past its memory bound is replaced from its first moment. The
// guard used to look at a host only ten minutes after it started, so a
// host that grew a gigabyte in two seconds was ended by the system before
// the guard saw it, and the guard never fired at all (#6458: 930 host
// starts over a day, 70 of them killed by the kernel's OOM killer).
//
// Only the bound and the time a host may be replaced in are set here: the
// guard's own timing is what this is about.
func TestHeavyHostIsReplacedFromItsStart(t *testing.T) {
	wasMB, wasEvery := heavyMB, heavyEvery
	heavyMB, heavyEvery = 1, 20*time.Millisecond
	t.Cleanup(func() { heavyMB, heavyEvery = wasMB, wasEvery })
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	was := recycles.Load()
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(30 * time.Second)
	for recycles.Load() == was {
		if time.Now().After(until) {
			t.Fatal("a host past its memory bound was not replaced in its first thirty seconds")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
