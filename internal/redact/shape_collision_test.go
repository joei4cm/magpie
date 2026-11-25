package redact

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// #7723: a made-up SECRET key that shares a lowercase substring with a
// USERNAME stand-in was mis-restored: restoreShapes replaced the
// stand-in anywhere in a string (whole=nil), so the substring inside
// the bigger made-up key went out as that user name's real value — a
// cross-value leak, worse than an unrestored one. A stand-in is a whole
// name: it is put back only where it stands alone (wholeAddr), as an
// address's already was.
func TestStandInInsideAnotherShapeIsLeftAlone(t *testing.T) {
	// a USERNAME stand-in as standIn() registers it: eight lowercase
	// letters, found in a string in any case
	mu.Lock()
	values["standinn"] = "the-user-name"
	shapes["standinn"] = "USERNAME"
	kinds["USERNAME"] = true
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		delete(values, "standinn")
		delete(shapes, "standinn")
		delete(kinds, "USERNAME")
		mu.Unlock()
	})

	// inside a bigger made-up key, the stand-in's letters are no stand-in
	for _, s := range []string{
		"AbCstandinnDefGh",     // lowercase run inside mixed case
		"standinntheuser-name", // the stand-in at the start
		"tail-endstandinn",     // at the end
		"a standinn b",         // wait — this one IS whole
	} {
		got := restoreShapes(s, nil)
		if s == "a standinn b" {
			if !strings.Contains(got, "the-user-name") {
				t.Errorf("a whole stand-in was not put back: %q", got)
			}
			continue
		}
		if strings.Contains(got, "the-user-name") {
			t.Errorf("stand-in replaced inside another string: %q -> %q", s, got)
		}
	}
}

// The same rule on the masking side (outsideShapes): a candidate secret
// that overlaps a stand-in's letters inside another made-up key is still
// masked, while one overlapping a whole stand-in is left to it.
func TestOutsideShapesSkipsOnlyWholeStandIns(t *testing.T) {
	mu.Lock()
	values["standinn"] = "the-user-name"
	shapes["standinn"] = "USERNAME"
	kinds["USERNAME"] = true
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		delete(values, "standinn")
		delete(shapes, "standinn")
		delete(kinds, "USERNAME")
		mu.Unlock()
	})

	// letters inside a word: the span over them is not the stand-in's
	if kept := outsideShapes("AbCstandinnDefG", []span{{start: 0, end: 16}}); len(kept) != 1 {
		t.Errorf("a span over a stand-in's letters inside another string was skipped: %v", kept)
	}
	// a whole stand-in keeps what overlaps it to itself
	if kept := outsideShapes("a standinn b", []span{{start: 2, end: 10}}); len(kept) != 0 {
		t.Errorf("a span over a whole stand-in was not left to it: %v", kept)
	}
}

// The stream case from the report: a SECRET made-up key that holds a
// USERNAME stand-in's letters is split across two SSE events; before
// the fix the held piece was sent out with the user name's real value
// inside it.
func TestStreamShapeWithStandInLettersKept(t *testing.T) {
	mu.Lock()
	values["standinn"] = "the-user-name"
	shapes["standinn"] = "USERNAME"
	kinds["USERNAME"] = true
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		delete(values, "standinn")
		delete(shapes, "standinn")
		delete(kinds, "USERNAME")
		mu.Unlock()
	})
	// a SECRET fake holding the stand-in's letters
	const realToken = "Dtih9AuIir4vbbPUHmsKsJRxoA07FXBi"
	fake := "AAAstandinnBBB" + "012345678901234" // holds "standinn"
	mu.Lock()
	values[fake] = realToken
	shapes[fake] = "SECRET"
	kinds["SECRET"] = true
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		delete(values, fake)
		delete(shapes, fake)
		delete(kinds, "SECRET")
		mu.Unlock()
	})

	cut := 15
	ev1 := `data: {"choices":[{"delta":{"content":"your token is ` + fake[:cut] + `"}}]}` + "\n\n"
	ev2 := `data: {"choices":[{"delta":{"content":"` + fake[cut:] + ` ok"}}]}` + "\n\n"
	rec := httptest.NewRecorder()
	rw := NewWriter(rec)
	rw.Write([]byte(ev1))
	rw.Write([]byte(ev2))
	rw.Finish()
	out := rec.Body.String()
	if strings.Contains(out, "the-user-name") {
		t.Errorf("the stand-in's real value leaked into the stream:\n%s", out)
	}
	if !strings.Contains(out, realToken) {
		t.Errorf("the made-up secret was not restored:\n%s", out)
	}
}
