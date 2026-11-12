package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/yetone/magpie/internal/provider"
)

// sharesBody says whether part's bytes lie inside whole's.
func sharesBody(part, whole []byte) bool {
	p, s := uintptr(unsafe.Pointer(unsafe.SliceData(part))), uintptr(unsafe.Pointer(unsafe.SliceData(whole)))
	return len(part) > 0 && p >= s && p+uintptr(len(part)) <= s+uintptr(len(whole))
}

// heldCount is how many bodies heldBodies lists now.
func heldCount() int {
	if l := heldBodies.list.Load(); l != nil {
		return len(*l)
	}
	return 0
}

// A request's messages decoded as raw values, as each of its readers
// decodes them (the images, the conversation's id, the prompt, the edits),
// are the body's own bytes while it is served, not a copy each: a 1.5 MB
// body allocated ~60 MB on its way through a group, and three agents
// sending such bodies held the heap at ~310 MB. Once the request is done,
// a decode copies again, and an append to a shared value never writes into
// the body.
func TestRequestBodyRawValuesShareIt(t *testing.T) {
	before := heldCount()
	sent := memoBody(t.Name())
	ctx, done := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(sent)).WithContext(ctx)
	body, ok := (&Server{}).readRequestBody(httptest.NewRecorder(), r, provider.Chat, 0)
	if !ok {
		t.Fatal("body not read")
	}
	var q struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := decodeJSON(body, &q); err != nil || len(q.Messages) != 1 {
		t.Fatalf("decode: %v, %d messages", err, len(q.Messages))
	}
	if !sharesBody(q.Messages[0], body) {
		t.Fatal("a message decoded from the request's body while it is served is a copy of it")
	}
	var m map[string]json.RawMessage
	if err := decodeJSON(q.Messages[0], &m); err != nil || !sharesBody(m["content"], body) {
		t.Fatalf("a value decoded from a shared one is a copy (%v)", err)
	}
	var small struct {
		Model json.RawMessage `json:"model"`
	}
	if err := decodeJSON(body, &small); err != nil || sharesBody(small.Model, body) {
		t.Fatalf("a small value shares the body, which it would keep alive if kept (%v)", err)
	}
	// the body whole, shared twice: an append to one never shows in the
	// other, through the room past the body's end
	var w1, w2 json.RawMessage
	if decodeJSON(body, &w1) != nil || decodeJSON(body, &w2) != nil || !sharesBody(w1, body) {
		t.Fatal("the body decoded whole is not shared")
	}
	a1, a2 := append(w1, 'x'), append(w2, 'y')
	if a1[len(body)] != 'x' || a2[len(body)] != 'y' {
		t.Fatal("two appends to the shared body wrote over each other")
	}
	kept := bytes.Clone(body)
	_ = append(m["content"], `,"x"`...)
	if !bytes.Equal(body, kept) {
		t.Fatal("an append to a shared value wrote into the body")
	}

	done()
	for deadline := time.Now().Add(5 * time.Second); heldCount() > before; {
		if time.Now().After(deadline) {
			t.Fatal("the body is still held after its request ended")
		}
		time.Sleep(time.Millisecond)
	}
	q.Messages = nil
	if err := decodeJSON(body, &q); err != nil || sharesBody(q.Messages[0], body) {
		t.Fatalf("a body whose request ended is still shared (%v)", err)
	}
}

// sharesString says whether s's bytes lie inside whole's.
func sharesString(s string, whole []byte) bool {
	p := uintptr(unsafe.Pointer(unsafe.StringData(s)))
	b := uintptr(unsafe.Pointer(unsafe.SliceData(whole)))
	return len(s) > 0 && p >= b && p+uintptr(len(s)) <= b+uintptr(len(whole))
}

// A large string of a body being served is the body's own bytes: an image
// turn's pictures are most of its body, and unquoteJSON copied each of them
// once more to be read (#5748). A small one is still copied — it would keep
// the whole body alive if it were kept — and a body whose request ended is
// not shared either.
func TestHeldBodySharesItsLargeStrings(t *testing.T) {
	before := heldCount()
	big, small := strings.Repeat("a", heldShareLeast), "a short string"
	sent := []byte(`{"model":"m","messages":[{"role":"user","content":"` + big + `"},{"role":"user","content":"` + small + `"}]}`)
	ctx, done := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(sent)).WithContext(ctx)
	body, ok := (&Server{}).readRequestBody(httptest.NewRecorder(), r, provider.Chat, 0)
	if !ok {
		t.Fatal("body not read")
	}
	var q struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := decodeJSON(body, &q); err != nil || len(q.Messages) != 2 {
		t.Fatalf("decode: %v, %d messages", err, len(q.Messages))
	}
	if q.Messages[0].Content != big || q.Messages[1].Content != small {
		t.Fatal("the strings decoded are not the strings sent")
	}
	if !sharesString(q.Messages[0].Content, body) {
		t.Fatal("a large string decoded from the request's body while it is served is a copy of it")
	}
	if sharesString(q.Messages[1].Content, body) {
		t.Fatal("a small string shares the body, which it would keep alive if kept")
	}

	done()
	for deadline := time.Now().Add(5 * time.Second); heldCount() > before; {
		if time.Now().After(deadline) {
			t.Fatal("the body is still held after its request ended")
		}
		time.Sleep(time.Millisecond)
	}
	var again struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := decodeJSON(body, &again); err != nil || sharesString(again.Messages[0].Content, body) {
		t.Fatalf("a body whose request ended is still shared (%v)", err)
	}
}

// Only a request's own body is shared: bytes decodeJSON is handed from
// anywhere else — a buffer a stream fills again — are copied, so a raw
// value outlives what is written over them.
func TestDecodeJSONCopiesWhatIsNotHeld(t *testing.T) {
	buf := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", validMemoMin) + `"}]}`)
	var q struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := decodeJSON(buf, &q); err != nil {
		t.Fatal(err)
	}
	want := string(q.Messages[0])
	copy(buf, bytes.Repeat([]byte("y"), len(buf)))
	if string(q.Messages[0]) != want {
		t.Fatal("a raw value decoded from bytes not held changed with them")
	}
}

// heldBodies is bounded: requests whose context never ends, or more than
// heldBodiesMost at once, go without sharing rather than growing the list.
func TestHeldBodiesBounded(t *testing.T) {
	before := heldCount()
	holdBody(context.Background(), memoBody(t.Name()))
	if heldCount() != before {
		t.Fatal("a body whose context never ends was held")
	}
	ctx, done := context.WithCancel(context.Background())
	defer done()
	for i := range heldBodiesMost + 10 {
		holdBody(ctx, memoBody(t.Name()+string(rune('a'+i%26))))
	}
	if n := heldCount(); n > heldBodiesMost {
		t.Fatalf("%d bodies held, past %d", n, heldBodiesMost)
	}
	done()
	for deadline := time.Now().Add(5 * time.Second); heldCount() > before; {
		if time.Now().After(deadline) {
			t.Fatalf("%d bodies still held after their requests ended", heldCount()-before)
		}
		time.Sleep(time.Millisecond)
	}
}
