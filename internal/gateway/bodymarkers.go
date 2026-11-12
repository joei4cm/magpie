package gateway

import (
	"bytes"
	"encoding/binary"
	"math/bits"
	"sync/atomic"
	"unsafe"
)

// The gateway asks a long request body for the same markers again and
// again on its way through it: forVendor looks for six of them, the
// effort update for one, the compaction for one, the Lite lift for one,
// and each of those searches walked every byte of the conversation
// (#4894). A long Codex turn is 4 MB and more, and the searches add up:
// they are found in one pass over the body, and what was found is kept as
// long as the body is held (holdBody: the body as read, 32 KB or more),
// so the later asks of it walk no bytes at all. A body the gateway
// rewrote is a new one and is not held: it is searched for again.

// The markers, as the bytes they are written with. A marker's bytes
// start with a quote, so it can only be where the body has one.
const (
	markerChatMetadata    = `"internal_chat_message_metadata_passthrough"`
	markerEncryptedArgs   = `"encrypted_function_args"`
	markerStreamOptions   = `"stream_options"`
	markerConfigUpdate    = `"configuration_update"`
	markerOutputReplay    = `"output.encrypted_content"`
	markerEncrypted       = `"encrypted_content"`
	markerCompactionAsk   = `"compaction_trigger"`
	markerCompaction      = `"compaction`
	markerAdditionalTools = `"additional_tools"`
	markerContext         = `"context"`
	markerReasoning       = `"reasoning"`
	markerAnnotations     = `"annotations"`
	markerSpawnAgent      = `"spawn_agent"`
	markerDeveloper       = `"developer"`
	markerAccessPrograms  = `"access_programs"`
	markerToolSearchCall  = `"tool_search_call"`
	markerCustomToolCall  = `"custom_tool_call"`
	markerDisplay         = `"display"`
	markerOutputConfig    = `"output_config"`
	markerSystem          = `"system"`
	markerToolAddition    = `"tool_addition"`
	markerToolRemoval     = `"tool_removal"`
	markerToolReference   = `"tool_reference"`
	markerToolDefinition  = `"tool_definition"`
	markerToolUse         = `"tool_use"`
	markerRedacted        = `"redacted_thinking"`
	markerEffort          = `"effort"`
	markerCacheKey        = `"promptCacheKey"`
	markerTools           = `"tools"`
	markerTool            = `"tool`
	markerAssistant       = `"assistant"`
	markerAdvisor         = `"advisor_`
	markerAnthropicBeta   = `"anthropic_beta"`
	markerMaxTokens       = `"max_completion_tokens"`
	markerID              = `"id"`
)

// bodyMarker is a marker of a body: the bytes it is written with, the
// eight of them the scan compares, the mask of the bytes it compares (a
// marker of fewer than eight bytes is compared as far as it goes), and
// the bit it has in the set of markers found.
type bodyMarker struct {
	needle string
	pat    uint64 // the needle's bytes, little endian
	mask   uint64 // the bytes of the needle to compare
	bit    uint64
}

// bodyMarkers are the markers the gateway looks for in a request body,
// each with the bit it has in the set of markers found: the bit is the
// marker's own place in this list, so a found one is named by it.
var bodyMarkers = func() []bodyMarker {
	needles := []string{
		markerChatMetadata,
		markerEncryptedArgs,
		markerStreamOptions,
		markerConfigUpdate,
		markerOutputReplay,
		markerEncrypted,
		markerCompactionAsk,
		markerCompaction,
		markerAdditionalTools,
		markerContext,
		markerReasoning,
		markerAnnotations,
		markerSpawnAgent,
		markerDeveloper,
		markerAccessPrograms,
		markerToolSearchCall,
		markerCustomToolCall,
		markerDisplay,
		markerOutputConfig,
		markerSystem,
		markerToolAddition,
		markerToolRemoval,
		markerToolReference,
		markerToolDefinition,
		markerToolUse,
		markerRedacted,
		markerEffort,
		markerCacheKey,
		markerTools,
		markerTool,
		markerAssistant,
		markerAdvisor,
		markerAnthropicBeta,
		markerMaxTokens,
		markerID,
	}
	ms := make([]bodyMarker, 0, len(needles))
	for i, n := range needles {
		pat, mask := patternOf(n)
		ms = append(ms, bodyMarker{needle: n, pat: pat, mask: mask, bit: 1 << uint(i)})
	}
	return ms
}()

// bodyMarkerBits is each marker's bit, by its bytes.
var bodyMarkerBits = func() map[string]uint64 {
	m := make(map[string]uint64, len(bodyMarkers))
	for _, k := range bodyMarkers {
		m[k.needle] = k.bit
	}
	return m
}()

// bodyMarkerMask is the gateway's markers by the byte their bytes have
// after the quote: a quote in the body is looked at only when the byte
// after it may be a marker's.
var bodyMarkerMask = func() [256]uint64 {
	var m [256]uint64
	for i, k := range bodyMarkers {
		m[k.needle[1]] |= 1 << uint(i)
	}
	return m
}()

// bodyMarkerMost bounds how many markers a caller may name of its own.
const bodyMarkerMost = 32

// patternOf is a needle's bytes, little endian, and the mask of the bytes
// to compare: a needle of fewer than eight bytes is compared as far as it
// goes, and one of a byte or less is not a marker at all — there is no
// quote to look for it by.
func patternOf(needle string) (pat, mask uint64) {
	if len(needle) < 2 {
		return 0, 0
	}
	n := len(needle)
	if n > 8 {
		n = 8
	}
	for i := 0; i < n; i++ {
		pat |= uint64(needle[i]) << (8 * uint(i))
	}
	if len(needle) >= 8 {
		return pat, ^uint64(0)
	}
	return pat, uint64(1)<<(8*len(needle)) - 1
}

// quoteBytes, oneBytes and highBytes are what the scan reads the body's
// bytes with: a word's bytes each compared with a quote, the borrow a
// word's zero byte makes, and the high bit of a byte.
const (
	quoteBytes = 0x0101010101010101 * '"'
	oneBytes   = 0x0101010101010101
	highBytes  = 0x8080808080808080
)

// scanMarkers is which of the markers body holds, found in one pass over
// it: a marker's bytes start with a quote, so it can only be where the
// body has one, and the body's quotes are found eight bytes at a time.
//
// The marker's bit is its own place in the list, so a found one is named
// by it. A marker whose bytes are one or less is not found: it is
// searched for as it was before.
func scanMarkers(body []byte, markers []bodyMarker, mask *[256]uint64) uint64 {
	if f := markerScanned.Load(); f != nil {
		(*f)(body)
	}
	var found uint64
	n := len(body)
	for i := 0; i < n; i += 8 {
		// the word's bytes, and zeros after the ones the body has left
		v := wordAt(body, i) ^ quoteBytes
		for x := (v - oneBytes) &^ v & highBytes; x != 0; {
			p := i + bits.TrailingZeros64(x)>>3
			x &= x - 1
			if p+1 >= n {
				continue // no byte after the quote: no marker's bytes are there
			}
			u := wordAt(body, p)
			for m := mask[body[p+1]]; m != 0; {
				bit := m & -m
				m ^= bit
				k := markers[bits.TrailingZeros64(bit)]
				if u&k.mask != k.pat {
					continue
				}
				if p+len(k.needle) <= n && string(body[p:p+len(k.needle)]) == k.needle {
					found |= k.bit
				}
			}
		}
	}
	return found
}

// wordAt is the eight bytes of body at i, and zeros after the ones the
// body has left: a body's last word may be shorter than eight bytes, and
// the bytes past it are no quote.
func wordAt(body []byte, i int) uint64 {
	if i+8 <= len(body) {
		return binary.LittleEndian.Uint64(body[i:])
	}
	var w uint64
	for k := i; k < len(body); k++ {
		w |= uint64(body[k]) << (8 * (k - i))
	}
	return w
}

// bodyHas reports whether body holds the needle's bytes, as bytes.Contains
// does.
//
// The needle is one of the gateway's own markers: it is found in the one
// pass over the body that finds all of them, and what was found is kept
// as long as the body is held. A body that isn't held is searched for as
// it was before.
func bodyHas(body []byte, needle string) bool {
	bit, known := bodyMarkerBits[needle]
	if !known {
		return bytes.Contains(body, []byte(needle))
	}
	bits, held := heldMarkerBits(body)
	if !held {
		return bytes.Contains(body, []byte(needle))
	}
	return bits&bit != 0
}

// bodyHasAny reports whether body holds any of the needles' bytes, found
// in one pass over it.
//
// The needles are the gateway's own markers and any others named; a
// needle that doesn't start with a quote is searched for as it was
// before, as the scan looks for the markers at the body's quotes.
func bodyHasAny(body []byte, needles ...string) bool {
	var mine uint64
	var others []bodyMarker
	var plain []string
	for _, n := range needles {
		switch bit, known := bodyMarkerBits[n]; {
		case known:
			mine |= bit
		case len(n) < 2 || n[0] != '"':
			plain = append(plain, n)
		case len(others) >= bodyMarkerMost:
			plain = append(plain, n)
		default:
			pat, mask := patternOf(n)
			others = append(others, bodyMarker{needle: n, pat: pat, mask: mask})
		}
	}
	if mine != 0 {
		// the gateway's own markers first: what was found of a body being
		// served is known already, and a scan over it is one pass
		if bits, held := heldMarkerBits(body); held {
			if bits&mine != 0 {
				return true
			}
		} else if scanMarkers(body, bodyMarkers, &bodyMarkerMask)&mine != 0 {
			return true
		}
	}
	for _, n := range plain {
		if bytes.Contains(body, []byte(n)) {
			return true
		}
	}
	if len(others) == 0 {
		return false
	}
	// the markers the caller named, in one pass of their own
	var mask [256]uint64
	for i := range others {
		others[i].bit = 1 << uint(i)
		mask[others[i].needle[1]] |= 1 << uint(i)
	}
	return scanMarkers(body, others, &mask) != 0
}

// heldMarkerBits is which of the gateway's markers body holds, found in
// one pass over it and kept as long as the body is held. ok is false when
// body isn't one being served: a body that isn't held is searched for as
// it was before.
func heldMarkerBits(body []byte) (uint64, bool) {
	key := unsafe.SliceData(body)
	// the body is looked for in the held list before what was found of it
	// is read: a body that has gone is not one any more, and its memory
	// may be given to another body
	if !heldBodyIs(body) {
		return 0, false
	}
	heldBodies.Lock()
	e, known := heldBodies.markers[key]
	heldBodies.Unlock()
	if known && e.n == len(body) {
		return e.bits, true
	}
	bits := scanMarkers(body, bodyMarkers, &bodyMarkerMask)
	heldBodies.Lock()
	defer heldBodies.Unlock()
	// only while the body is held: a released body may be freed, and its
	// memory given to another body that would read as this one
	if !heldBodyIs(body) {
		return 0, false
	}
	if heldBodies.markers == nil {
		heldBodies.markers = map[*byte]heldMarkers{}
	}
	heldBodies.markers[key] = heldMarkers{n: len(body), bits: bits}
	return bits, true
}

// heldBodyIs reports whether body, whole, is one holdBody holds.
func heldBodyIs(body []byte) bool {
	l := heldBodies.list.Load()
	if l == nil {
		return false
	}
	key := unsafe.SliceData(body)
	for _, b := range *l {
		if unsafe.SliceData(b) == key && len(b) == len(body) {
			return true
		}
	}
	return false
}

// markerScanned, when set (tests), is told of each body scanned for the
// markers.
var markerScanned atomic.Pointer[func(body []byte)]
