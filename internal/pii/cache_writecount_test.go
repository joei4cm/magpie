package pii

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// #6457: the file of what the model learned holds the whole cache, so
// every write is the whole cache. It is written at most once every
// saveEvery now.
//
// This test names nothing the code before the change did not have, so
// it can be run on that code alone: copied to a tree at origin/main
// it is red — five answers, five writes of the file — and on this tree
// it is green. It counts the writes by the file they land on, since
// every write renames a new file over the old one.
func TestCacheFileIsNotWrittenForEveryAnswer(t *testing.T) {
	fakeInstall(t, "ok", false)
	after := saveAfter
	saveAfter = 10 * time.Millisecond
	t.Cleanup(func() { saveAfter = after })
	writes := 0
	var prev os.FileInfo
	for i := 0; i < 5; i++ {
		if _, err := Find([]string{fmt.Sprintf("hi Alice %d", i)}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Millisecond)
		st, err := os.Stat(cachePath())
		if err != nil {
			continue
		}
		if prev == nil || !os.SameFile(prev, st) {
			writes++
			prev = st
		}
	}
	if writes > 1 {
		t.Fatalf("5 answers: %d writes of the file, want 1: the file is written at most once every saveEvery", writes)
	}
}
