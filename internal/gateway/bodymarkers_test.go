package gateway

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
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
