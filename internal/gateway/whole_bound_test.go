package gateway

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A reply wanted whole — a compaction's summary (#2274) — is held to its
// end with no byte bound of its own, so a long history's summary is held in
// full for every in-flight request (#3069). Past wholeMost it fails as one
// that ended incomplete does: the model's doing, not the account's, so the
// next candidate is asked and nobody rests (gateway.go), and the stream is
// stopped rather than the heap holding it for the request's life.
func TestReplyWantedWholeIsBound(t *testing.T) {
	whole := func() (*holdWriter, *bool) {
		var h holdWriter
		h.wantWhole, h.stream = true, true
		h.since = time.Now()
		var stopped bool
		h.stop = func() { stopped = true }
		return &h, &stopped
	}
	// one byte over the bound fails and stops the stream
	h, stopped := whole()
	h.held.Write(bytes.Repeat([]byte("x"), wholeMost+1))
	h.scan()
	if !*stopped {
		t.Fatal("a reply held past wholeMost went on being read")
	}
	if h.failure != http.StatusBadGateway {
		t.Errorf("a reply held past wholeMost failed with %d, want %d", h.failure, http.StatusBadGateway)
	}
	if !strings.HasPrefix(h.failMsg, incompleteReply) {
		t.Errorf("a reply held past wholeMost said %q, which does not open with %q: the next candidate would not be asked and the account would rest", h.failMsg, incompleteReply)
	}
	// at the bound it is still held, waiting for the rest of the reply
	h, stopped = whole()
	h.held.Write(bytes.Repeat([]byte("x"), wholeMost))
	h.scan()
	if *stopped || h.failure != 0 {
		t.Errorf("a reply held at wholeMost failed (%d %q) and stopped: %v", h.failure, h.failMsg, *stopped)
	}
}
