package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"runtime"
	"strings"
	"testing"
)

type ptrMarshaler struct{ n int }

func (p *ptrMarshaler) MarshalJSON() ([]byte, error) { return []byte(`"ptr"`), nil }

type valMarshaler []int

func (valMarshaler) MarshalJSON() ([]byte, error) { return []byte(`"whole"`), nil }

// marshalBody writes what json.Marshal writes, every kind of value a
// builder puts in a body included.
func TestMarshalBodyIsJSONMarshal(t *testing.T) {
	var nilList []any
	bodies := func() []map[string]any {
		return []map[string]any{
			{},
			{"model": "m", "stream": true, "max_tokens": 10, "temperature": 0.5},
			{"messages": []any{map[string]any{"role": "user", "content": "<a&b> \u2028 \u2029 \x01 é"}, map[string]any{"role": "assistant", "content": nil}}},
			{"messages": []map[string]any{{"role": "user"}, {"role": "system", "content": []any{"x", 1}}}},
			{"one": []any{"only"}, "none": []any{}, "nil": nilList, "nilv": nil},
			{"bytes": []byte("raw bytes"), "raw": json.RawMessage(`{"a":1}`), "raws": []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`{"b":2}`)}},
			{"ptrs": []ptrMarshaler{{1}, {2}}, "whole": valMarshaler{1, 2, 3}, "strs": []string{"a", "<b>"}},
			{"<k>": 1, "a\u2028": 2, "z": []int{1, 2, 3}},
		}
	}
	// kept whole, measured from the first item on, and from the second
	t.Cleanup(func() { bodyKept = 4 << 20 })
	for _, kept := range []int{4 << 20, 0, 1} {
		bodyKept = kept
		for i, b := range bodies() { // fresh: a spent list is zeroed
			want, werr := json.Marshal(b)
			got, gerr := marshalBody(b, "messages")
			if (werr == nil) != (gerr == nil) || !bytes.Equal(got, want) {
				t.Errorf("kept %d, body %d:\n got %s (%v)\nwant %s (%v)", kept, i, got, gerr, want, werr)
			}
		}
	}
	for _, kept := range []int{4 << 20, 0} {
		bodyKept = kept
		for _, bad := range []map[string]any{
			{"messages": []any{1, math.NaN()}},
			{"x": math.Inf(1)},
			{"ch": []any{"a", make(chan int)}},
		} {
			if _, err := marshalBody(bad, ""); err == nil {
				t.Errorf("kept %d: marshalBody(%v) took what json.Marshal refuses", kept, bad)
			}
		}
	}
}

// A 45 MB conversation is built without the heap growing past the body
// and a few MiB: json.Marshal's doubling buffer took it from 45 to 316
// MiB, and keeping every written item for the body was a copy more
// (#4004).
func TestMarshalBodyAllocatesTwoBodies(t *testing.T) {
	if raceOn {
		t.Skip("under -race sync.Pool drops what json.Marshal's encoders keep, so each item allocates afresh")
	}
	big := strings.Repeat("tool output line\n", 1<<14)
	msgs := make([]any, 0, 170)
	for range 170 {
		msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": "c", "content": big})
	}
	body := map[string]any{"model": "m", "messages": msgs}
	peak := func(f func() []byte) (uint64, int) {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		before := m.TotalAlloc
		n := len(f())
		runtime.ReadMemStats(&m)
		return m.TotalAlloc - before, n
	}
	stdAlloc, n := peak(func() []byte { b, _ := json.Marshal(body); return b })
	ownAlloc, _ := peak(func() []byte { b, _ := marshalBody(body, ""); return b })
	t.Logf("%d MiB body: json.Marshal allocated %d MiB, marshalBody %d MiB", n>>20, stdAlloc>>20, ownAlloc>>20)
	// the body, the items kept for it and the encoder's buffer: no copy
	// of every item beside the body (#4004)
	if ownAlloc > uint64(n)*3/2 {
		t.Errorf("marshalBody allocated %d MiB for a %d MiB body, want at most 1.5x", ownAlloc>>20, n>>20)
	}
}

// spentProbe is an item of a list that, as it is written, looks whether
// the item before it was let go.
type spentProbe struct {
	list *[]*spentProbe
	i    int
	held *[]int
}

func (p *spentProbe) MarshalJSON() ([]byte, error) {
	if p.i > 0 && (*p.list)[p.i-1] != nil {
		*p.held = append(*p.held, p.i-1)
	}
	return []byte(`1`), nil
}

// The conversation a builder made for the body is let go an item at a
// time as the body fills, not held beside it to the end: built for Chat,
// a 45 MiB Codex conversation's messages, strings joined from its parts
// among them, were a copy more at the body's peak (#4004). A list not
// named spent is left as it was.
// A data URL is written into the body a piece at a time, not made as one
// string first and copied in again: on an image turn the URLs are most of
// the body, so a 20 MiB request copied them twice more (#5748). The bytes
// are encoding/json's either way — through the body's writer, through
// encoding/json itself, and for every way of keeping a list's items.
func TestDataURLValueIsJSONMarshalOfTheURL(t *testing.T) {
	url := func(p Part) string {
		if p.URL != "" && p.Data == "" {
			return p.URL
		}
		return "data:" + p.MediaType + ";base64," + p.Data
	}
	parts := []Part{
		{Kind: Image, MediaType: "image/png", Data: "AAAA"},
		{Kind: Image, MediaType: "image/jpeg", Data: `a"b\c` + "\n"},  // not base64: it escapes
		{Kind: Image, MediaType: "image/png", Data: "h\u00e9llo <&>"}, // not ASCII, and HTML's own
		{Kind: Image, MediaType: "image/png", Data: "a\u2028b"},       // U+2028, escaped either way
		{Kind: Image, URL: "https://example.com/a.png?x=1&y=2"},       // a URL as it came
		{Kind: Image, MediaType: "", Data: ""},                        // nothing to say it with
	}
	for _, p := range parts {
		v, want := dataURL(p), url(p)
		body := func() map[string]any {
			return map[string]any{"type": "image_url", "image_url": map[string]any{"url": v, "detail": "auto"}}
		}
		wantBody, err := json.Marshal(map[string]any{"type": "image_url", "image_url": map[string]any{"url": want, "detail": "auto"}})
		if err != nil {
			t.Fatal(err)
		}
		wantJSON, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := json.Marshal(v); err != nil || !bytes.Equal(got, wantJSON) {
			t.Errorf("json.Marshal(%q): got %s (%v), want %s", want, got, err, wantJSON)
		}
		n, ok := jsonSize(v)
		got, err := appendJSONValue(nil, v)
		if !ok || err != nil || n != len(got) || !bytes.Equal(got, wantJSON) {
			t.Errorf("appendJSONValue(%q): %d measured, %s (%v), want %d %s", want, n, got, err, len(wantJSON), wantJSON)
		}
		t.Cleanup(func() { bodyKept = 4 << 20 })
		for _, kept := range []int{4 << 20, 0, 1} {
			bodyKept = kept
			if got, err := marshalBody(body(), ""); err != nil || !bytes.Equal(got, wantBody) {
				t.Errorf("kept %d, marshalBody(%q): got %s (%v), want %s", kept, want, got, err, wantBody)
			}
		}
	}
}

// No URL of a turn's images is made first: an item's bytes are written
// where the picture's are read from. The URL was one string first, and the
// item's writer copied it in again, so a 2 MiB picture cost its URL and the
// bytes holding it — twice its size for one image, and a turn's images are
// most of its body (#5748).
func TestDataURLValueMakesNoURLFirst(t *testing.T) {
	if raceOn {
		t.Skip("under -race the pool the body's writer uses lets a quarter of what is put in it go, so every item allocates afresh")
	}
	p := Part{Kind: Image, MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("A"), 2<<20))}
	url := "data:" + p.MediaType + ";base64," + p.Data
	write := allocated(func() {
		marshalSized(map[string]any{"url": dataURL(p)})
	})
	if limit := uint64(len(url)) * 3 / 2; write > limit {
		t.Fatalf("a %d-byte URL written as an item allocated %d bytes: the URL is still made first", len(url), write)
	}
}

func TestMarshalBodyLetsSpentGo(t *testing.T) {
	var held []int
	list := make([]*spentProbe, 5)
	for i := range list {
		list[i] = &spentProbe{list: &list, i: i, held: &held}
	}
	other := []string{"a", "b"}
	got, err := marshalBody(map[string]any{"messages": list, "other": other}, "messages")
	if err != nil || string(got) != `{"messages":[1,1,1,1,1],"other":["a","b"]}` {
		t.Fatalf("wrote %s (%v)", got, err)
	}
	if len(held) > 0 {
		t.Errorf("items %v were still held when the next was written", held)
	}
	if other[0] != "a" || other[1] != "b" {
		t.Errorf("a list not spent was changed: %q", other)
	}
}
