package pii

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// entriesIn is how many entries the cache file at path holds.
func entriesIn(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	es, err := decodeCache(b)
	if err != nil {
		t.Fatalf("the cache file at %s: %v", path, err)
	}
	return len(es)
}

// #6457: the file of what the model learned was written whole every
// couple of seconds — 900 KB for a cache that changes by a few bytes
// between the writes — so a gateway answering every few seconds wrote
// 17 GB a day. It is written at most once every saveEvery now.
func TestCacheFileWrittenOnceForABurstOfAnswers(t *testing.T) {
	fakeInstall(t, "ok", false)
	after, every := saveAfter, saveEvery
	saveAfter, saveEvery = 10*time.Millisecond, time.Hour
	t.Cleanup(func() { saveAfter, saveEvery = after, every })
	var writes atomic.Int32
	written := func() { writes.Add(1) }
	cacheWritten.Store(&written)
	t.Cleanup(func() { cacheWritten.Store(nil) })

	for i := 0; i < 5; i++ {
		if _, err := Find([]string{fmt.Sprintf("hi Alice %d", i)}); err != nil {
			t.Fatal(err)
		}
		// past the debounce, so every answer would be written on its own
		time.Sleep(30 * time.Millisecond)
	}
	waitFor(t, "the first write", func() bool { return writes.Load() >= 1 })
	if n := writes.Load(); n != 1 {
		t.Fatalf("5 answers: %d writes of the file, want 1: the file is written at most once every saveEvery", n)
	}
}

// What was learned after the last write is written when the gateway
// ends: the file is written at most once every saveEvery while it runs,
// and the learning is kept for the next start.
func TestCacheFileIsWrittenWhenTheGatewayEnds(t *testing.T) {
	fakeInstall(t, "ok", false)
	after, every := saveAfter, saveEvery
	saveAfter, saveEvery = 10*time.Millisecond, time.Hour
	t.Cleanup(func() { saveAfter, saveEvery = after, every })

	if _, err := Find([]string{"hi Alice"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the cache file", func() bool { _, err := os.Stat(cachePath()); return err == nil })
	first := entriesIn(t, cachePath())
	if _, err := Find([]string{"Bob, Alice"}); err != nil {
		t.Fatal(err)
	}
	FlushCache()
	if second := entriesIn(t, cachePath()); second <= first {
		t.Fatalf("what was learned after the last write: %d entries in the file, want more than %d: it is written when the gateway ends", second, first)
	}
}
