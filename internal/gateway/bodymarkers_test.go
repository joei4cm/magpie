package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"time"
	"unsafe"
)

// markerBitsOf is what was found of a body, nil when none was.
func markerBitsOf(body []byte) *heldMarkers {
	heldBodies.Lock()
	defer heldBodies.Unlock()
	e, ok := heldBodies.markers[unsafe.SliceData(body)]
	if !ok || e.n != len(body) {
		return nil
	}
	return &e
}

// markerBody is a request body past validMemoMin, so it is one holdBody
// holds, with a long history in it and the six markers of forVendor in it.
func markerBody(tag string) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"` + tag + `","stream":true,"stream_options":{"reasoning_summary_delivery":"auto"},` +
		`"include":["output.encrypted_content"],"reasoning":{"context":"all_turns"},"input":[`)
	for i := 0; i < 400; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		switch i {
		case 0:
			b.WriteString(`{"type":"configuration_update","reasoning":{"effort":"high"}}`)
		case 1:
			b.WriteString(`{"type":"function_call","name":"shell","encrypted_function_args":[]}`)
		case 2:
			b.WriteString(`{"type":"message","role":"user","internal_chat_message_metadata_passthrough":{"turn_id":"t1"},` +
				`"content":[{"type":"input_text","text":"turn 2"}]}`)
		case 3:
			b.WriteString(`{"type":"function_call_output","call_id":"c1","output":"a.go","encrypted_content":"sealed"}`)
		default:
			b.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"turn `)
			b.WriteString(strings.Repeat("a long turn ", 20))
			b.WriteString(`"}]}`)
		}
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// plainBody is a request body past validMemoMin with no marker of the
// gateway's own in it.
func plainBody(tag string) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"` + tag + `","stream":true,"input":[`)
	for i := 0; i < 400; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"turn `)
		b.WriteString(strings.Repeat("a long turn ", 20))
		b.WriteString(`"}]}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// countMarkerScans counts the scans of bodies equal to b.
func countMarkerScans(t *testing.T, b []byte) *atomic.Int32 {
	var n atomic.Int32
	f := func(data []byte) {
		if bytes.Equal(data, b) {
			n.Add(1)
		}
	}
	markerScanned.Store(&f)
	t.Cleanup(func() { markerScanned.Store(nil) })
	return &n
}

// The scan finds what bytes.Contains finds: a marker's bytes are the
// marker's, wherever they are in the body, and nothing else is taken for
// one.
func TestScanMarkersIsBytesContains(t *testing.T) {
	bodies := []string{
		``,
		`{}`,
		`{"model":"m","input":[{"type":"message","content":"a turn"}]}`,
		`{"model":"m","stream_options":{"reasoning_summary_delivery":"auto"},"input":[]}`,
		`{"model":"m","input":[{"type":"configuration_update","reasoning":{"effort":"high"}}]}`,
		`{"model":"m","include":["output.encrypted_content"],"input":[]}`,
		`{"model":"m","input":[{"type":"function_call","encrypted_content":"sealed"}]}`,
		`{"model":"m","input":[{"type":"additional_tools","tools":[{"type":"function"}]}]}`,
		`{"model":"m","reasoning":{"context":"all_turns","effort":"high"},"input":[]}`,
		`{"model":"m","input":[{"type":"message","annotations":[{"type":"url_citation"}]}]}`,
		`{"model":"m","input":[{"type":"message","role":"developer","content":"rules"}]}`,
		`{"model":"m","input":[{"type":"compaction_trigger"}]}`,
		// not JSON, and the bytes of a marker in the middle of a word
		`not json at all "stream_options" and "compaction`,
		// a marker's bytes with the quote escaped
		`{"model":"m","input":[{"type":"message","content":"say \"custom\" now"}]}`,
		// a marker in a text that spells its word without the quotes
		`{"model":"m","input":[{"type":"message","content":"custom context reasoning"}]}`,
	}
	for _, b := range bodies {
		body := []byte(b)
		for _, m := range bodyMarkers {
			want := bytes.Contains(body, []byte(m.needle))
			got := scanMarkers(body, bodyMarkers, &bodyMarkerMask)&m.bit != 0
			if want != got {
				t.Errorf("%s in %q: found %v, want %v", m.needle, b, got, want)
			}
		}
	}
}

// A marker is found wherever it is in the body: at its start, at its end,
// and across the eight bytes the scan reads at a time.
func TestScanMarkersFindsAMarkerAnywhere(t *testing.T) {
	for _, m := range bodyMarkers {
		for off := 0; off < 24; off++ {
			body := append(bytes.Repeat([]byte("x"), off), m.needle...)
			body = append(body, bytes.Repeat([]byte("y"), 40)...)
			if scanMarkers(body, bodyMarkers, &bodyMarkerMask)&m.bit == 0 {
				t.Errorf("%s at %d was not found", m.needle, off)
			}
			// the same marker, quoted twice over, is found where the
			// second one is
			body = append(bytes.Repeat([]byte("x"), off), m.needle...)
			body = append(body, bytes.Repeat([]byte("y"), 40)...)
			body = append(body, m.needle...)
			if scanMarkers(body, bodyMarkers, &bodyMarkerMask)&m.bit == 0 {
				t.Errorf("%s at %d was not found after another one", m.needle, off)
			}
		}
	}
}

// The word a marker is written with, without the quotes around it, is
// not the marker: a turn's text that says custom offers no custom tool.
func TestScanMarkersNeedsTheQuote(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"type":"message","content":"custom context reasoning compaction"}]}`)
	for _, m := range bodyMarkers {
		if scanMarkers(body, bodyMarkers, &bodyMarkerMask)&m.bit != 0 {
			t.Errorf("%s was found in the text", m.needle)
		}
	}
}

// The markers of a body are found in one pass over it: a request that
// asks for six of them walks the body once, not once for each (#4894).
// The body was walked six times over before, once for every marker.
func TestMarkersAreFoundInOnePass(t *testing.T) {
	body := markerBody(t.Name())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	holdBody(ctx, body)
	scans := countMarkerScans(t, body)
	for _, m := range openaiOnly {
		if !bodyHasAny(body, m) {
			t.Fatalf("the body holds no %s", m)
		}
	}
	if n := scans.Load(); n != 1 {
		t.Fatalf("the markers of a %d-byte body were found in %d passes over it, want 1", len(body), n)
	}
}

// What was found of a body is kept as long as the body is held: the
// later asks of it walk no bytes at all.
func TestMarkersAreKeptWhileTheBodyIsHeld(t *testing.T) {
	body := markerBody(t.Name())
	ctx, cancel := context.WithCancel(context.Background())
	holdBody(ctx, body)
	scans := countMarkerScans(t, body)
	for range 4 {
		for _, m := range openaiOnly {
			bodyHas(body, m)
		}
	}
	if n := scans.Load(); n != 1 {
		t.Fatalf("the markers of a %d-byte body were found in %d passes over it, want 1", len(body), n)
	}
	// the body goes, and what was found of it with it
	cancel()
	for deadline := time.Now().Add(5 * time.Second); markerBitsOf(body) != nil; {
		if time.Now().After(deadline) {
			t.Fatal("what was found of a released body was kept")
		}
		time.Sleep(time.Millisecond)
	}
}

// A body that isn't held is searched for as it was before: the marker is
// found, and no pass over the body is kept.
func TestBodyHasOfAnUnheldBody(t *testing.T) {
	body := []byte(`{"model":"m","stream_options":{"reasoning_summary_delivery":"auto"},"input":[]}`)
	scans := countMarkerScans(t, body)
	if !bodyHas(body, markerStreamOptions) {
		t.Fatal("the body holds no stream_options")
	}
	if n := scans.Load(); n != 0 {
		t.Fatalf("a body that isn't held was scanned %d times, want 0", n)
	}
}

// A body held is asked for its markers as it was: the marker is found
// where it is in the body, and the ones it doesn't hold are not.
func TestBodyHasOfAHeldBody(t *testing.T) {
	body := plainBody(t.Name())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	holdBody(ctx, body)
	if bodyHas(body, markerStreamOptions) {
		t.Fatal("the body holds no stream_options")
	}
	with := append(append([]byte{}, body...), []byte(`,"stream_options":{}`)...)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	holdBody(ctx2, with)
	if !bodyHas(with, markerStreamOptions) {
		t.Fatal("the body holds stream_options")
	}
	if bodyHas(with, markerConfigUpdate) {
		t.Fatal("the body holds no configuration_update")
	}
}

// benchmarkBody is a long turn of the gateway's own markers: a long
// history first, and the six of forVendor at its end, where a scan that
// stops when it finds one has to walk the whole conversation to reach
// them.
func benchmarkBody(size int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"fake/m1","stream":true,"input":[`)
	for i := 0; b.Len() < size; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"turn `)
		b.WriteString(strings.Repeat("a long turn of the conversation ", 8))
		b.WriteString(`"}]}`)
	}
	b.WriteString(`],"stream_options":{"reasoning_summary_delivery":"auto"},` +
		`"include":["output.encrypted_content"],`)
	b.WriteString(`"input_tail":[{"type":"configuration_update","reasoning":{"effort":"high"}},` +
		`{"type":"function_call","name":"shell","encrypted_function_args":[]},` +
		`{"type":"message","role":"user","internal_chat_message_metadata_passthrough":{"turn_id":"t1"}},` +
		`{"type":"function_call_output","call_id":"c1","output":"a.go","encrypted_content":"sealed"}]}`)
	return []byte(b.String())
}

// The markers of a long turn are found in one pass over it: the gateway
// asks for six of them on its way through, and each ask walked every byte
// of the conversation before (#4894).
func BenchmarkBodyHasAny(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 8 << 20} {
		body := benchmarkBody(size)
		b.Run(fmt.Sprintf("%d bytes", size), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.Run("one pass", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					bodyHasAny(body, openaiOnly...)
				}
			})
			b.Run("a scan each", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					for _, m := range openaiOnly {
						bytes.Contains(body, []byte(m))
					}
				}
			})
		})
	}
}

// A marker is found wherever it is in the body, to its last byte: the
// scan reads eight bytes at a time, and the bytes at the end that make
// less than a word are read too. A marker of fewer than eight bytes is
// compared as far as it goes, and one that ends the body is found.
func TestScanMarkersAtTheEndOfABody(t *testing.T) {
	for _, m := range bodyMarkers {
		for k := range 24 {
			for _, tail := range []string{"", "y", `"}`, "yz"} {
				body := []byte(strings.Repeat("x", k) + m.needle + tail)
				want := bytes.Contains(body, []byte(m.needle))
				got := scanMarkers(body, bodyMarkers, &bodyMarkerMask)&m.bit != 0
				if want != got {
					t.Errorf("%s at %d in %q: found %v, want %v", m.needle, k, body, got, want)
				}
			}
		}
	}
}

// A needle that doesn't start with a quote is searched for as it was
// before: the scan looks for the markers at the body's quotes, and a
// needle of its own is looked for as a whole.
func TestBodyHasAnyOfANeedleWithoutAQuote(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"type":"message","content":"an image/hei here"}]}`)
	if !bodyHasAny(body, "image/hei") {
		t.Error("the needle without a quote was not found")
	}
	if bodyHasAny(body, `"image/hei"`) {
		t.Error("the needle with a quote was found")
	}
	if bodyHasAny(body, "image/heif") {
		t.Error("a needle the body doesn't hold was found")
	}
}

// The scan finds what bytes.Contains finds, over bodies made of the
// bytes a request is made of: quotes, the markser's own bytes, escapes
// and the lot, with a marker planted at a random place in one of them.
func TestScanMarkersIsBytesContainsOnRandomBodies(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	alphabet := []byte{'"', 'c', 'o', 'm', 'p', 'a', 't', 'i', 'n', 'e', 'r', 's', 'x', '\\', '{', '}', '[', ']', ':', ',', ' ', 0x00, 0xff, 0x80, 'g', 'd', 'v', 'l', 'u', 'f', 'y', 'z', '0', '9', '_', '.'}
	for range 50000 {
		body := make([]byte, rnd.IntN(64))
		for i := range body {
			body[i] = alphabet[rnd.IntN(len(alphabet))]
		}
		if rnd.IntN(2) == 0 {
			m := bodyMarkers[rnd.IntN(len(bodyMarkers))]
			off := rnd.IntN(len(body) + 1)
			body = append(body[:off], append([]byte(m.needle), body[off:]...)...)
		}
		got := scanMarkers(body, bodyMarkers, &bodyMarkerMask)
		for _, m := range bodyMarkers {
			want := bytes.Contains(body, []byte(m.needle))
			if found := got&m.bit != 0; want != found {
				t.Fatalf("%q: %s found %v, want %v", body, m.needle, found, want)
			}
		}
	}
}

// The needles a caller names are found as bytes.Contains finds them: a
// needle of its own, one that doesn't start with a quote, and the
// gateway's own markers, all in one pass over the body.
func TestBodyHasAnyIsBytesContainsOnRandomBodies(t *testing.T) {
	rnd := rand.New(rand.NewPCG(3, 4))
	needles := []string{`"tool_use"`, `"redacted_thinking"`, `"display"`, `"output_config"`,
		`"tool_addition"`, `"tool_removal"`, `"tool_reference"`, `"tool_definition"`,
		`"tool_search_call"`, `"custom_tool_call"`, `"reasoning"`, `"context"`,
		`"stream_options"`, `"compaction`, `"id"`, `"tool`, `""`, `x`, "image/hei"}
	alphabet := []byte{'"', 't', 'o', 'l', '_', 'u', 's', 'e', 'r', 'd', 'c', 'n', 'x', 'y', 'm', 'p', 'a', 'i', 'g', 'h', ' ', '\\', '{', '}', '[', ']', ':', ',', 0x00, 0xff}
	for range 20000 {
		body := make([]byte, rnd.IntN(80))
		for i := range body {
			body[i] = alphabet[rnd.IntN(len(alphabet))]
		}
		if rnd.IntN(2) == 0 {
			k := needles[rnd.IntN(len(needles))]
			off := rnd.IntN(len(body) + 1)
			body = append(body[:off], append([]byte(k), body[off:]...)...)
		}
		var sub []string
		for _, k := range needles {
			if rnd.IntN(3) == 0 {
				sub = append(sub, k)
			}
		}
		if len(sub) == 0 {
			sub = []string{needles[rnd.IntN(len(needles))]}
		}
		want := false
		for _, k := range sub {
			if bytes.Contains(body, []byte(k)) {
				want = true
				break
			}
		}
		if got := bodyHasAny(body, sub...); got != want {
			t.Fatalf("%q: %q found %v, want %v", body, sub, got, want)
		}
	}
}

// A body being served is asked for its markers as bytes.Contains finds
// them, the first time and the later ones alike: what was found of it is
// kept, and it is what is in it.
func TestBodyHasOfAHeldBodyOnRandomBodies(t *testing.T) {
	rnd := rand.New(rand.NewPCG(5, 6))
	alphabet := []byte{'"', 'c', 'o', 'm', 'p', 'a', 't', 'i', 'n', 'e', 'r', 's', 'x', '\\', '{', '}', '[', ']', ':', ',', ' ', 'g', 'd', 'v', 'l', 'u', 'f', 'y', 'z', '0', '9', '_', '.'}
	for range 200 {
		body := make([]byte, validMemoMin+rnd.IntN(4096))
		for i := range body {
			body[i] = alphabet[rnd.IntN(len(alphabet))]
		}
		for range rnd.IntN(4) {
			m := bodyMarkers[rnd.IntN(len(bodyMarkers))]
			off := rnd.IntN(len(body) - len(m.needle))
			copy(body[off:], m.needle)
		}
		ctx, cancel := context.WithCancel(context.Background())
		holdBody(ctx, body)
		for _, m := range bodyMarkers {
			want := bytes.Contains(body, []byte(m.needle))
			if got := bodyHas(body, m.needle); got != want {
				t.Fatalf("%q: %s found %v, want %v", body, m.needle, got, want)
			}
		}
		for _, m := range bodyMarkers {
			want := bytes.Contains(body, []byte(m.needle))
			if got := bodyHas(body, m.needle); got != want {
				t.Fatalf("%q: %s found %v, want %v", body, m.needle, got, want)
			}
		}
		cancel()
	}
}

// A long Codex request is served as it came: the gateway walks the body
// once for the markers, not once for each of them (#4894).
func BenchmarkLongCodexRequest(b *testing.B) {
	noRests()
	b.Cleanup(noRests)
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	b.Setenv("XDG_CACHE_HOME", b.TempDir())
	reply := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, reply)
	}))
	b.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		b.Fatal(err)
	}
	body := benchmarkBody(1 << 20)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		// the body is held as long as the request is served, and released
		// with it
		ctx, cancel := context.WithCancel(context.Background())
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(body))).WithContext(ctx)
		New().Handler().ServeHTTP(rec, req)
		cancel()
		if rec.Code != 200 {
			b.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		for deadline := time.Now().Add(5 * time.Second); heldCount() > 0; {
			if time.Now().After(deadline) {
				b.Fatal("the body was not released")
			}
			time.Sleep(100 * time.Microsecond)
		}
	}
}

// The passthrough is a member of the request, so its name is in the body
// quoted: a body that names it in its text, without the quotes, is left
// as it came, and a body that has the member has it taken out when the
// provider turned it away.
func TestRefusedPassthroughLeavesABodyWithoutTheMember(t *testing.T) {
	s := &Server{unfit: map[string]bool{}}
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"internal_chat_message_metadata_passthrough"}]}]}`)
	if got := s.withoutRefusedPassthrough("fake", provider.Responses, body); !bytes.Equal(got, body) {
		t.Fatalf("a body without the member was changed: %s", got)
	}
	s.markUnfit("fake", passthroughRefused(""), provider.Responses)
	with := []byte(`{"input":[{"type":"message","role":"user","internal_chat_message_metadata_passthrough":{"turn_id":"t1"}}]}`)
	got := s.withoutRefusedPassthrough("fake", provider.Responses, with)
	if bytes.Contains(got, []byte(passthroughKey)) {
		t.Fatalf("the member was not taken out: %s", got)
	}
}

// The markers have a bit each, and a word has as many bits as there are
// markers: a marker past the last one would take the bit of the first,
// and be found never.
func TestBodyMarkersHaveABitEach(t *testing.T) {
	if len(bodyMarkers) > 64 {
		t.Fatalf("%d markers, want at most 64: the bit of a marker past the last is the first's", len(bodyMarkers))
	}
	for i, m := range bodyMarkers {
		if m.bit != 1<<uint(i) {
			t.Fatalf("%s has the bit %d, want %d", m.needle, m.bit, 1<<uint(i))
		}
	}
}
