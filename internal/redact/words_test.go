package redact

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// wordsPara is a history as an agent sends one: build talk, routes, a
// customer and a vendor by name.
const wordsPara = `The build failed in internal/gateway at TestClientPathCoversHandler: the
read-only key asked for /magpie-ca.crt and got 404. The route was added in
e6f151507 with no clientPrefixes line, so the one-port page never learns it.
Reproduce with a gateway key whose model list holds a glob, then ask /v1/models
twice and diff the bodies. Customer Acme Trading Co said their invoice 2026-11
was rejected by the bank; vendor Foo Industries refunded it the same day. `

// wordList is a business list as long as the reporter's (#7227): 5415
// customer and vendor names, two of which the history holds.
func wordList(n int) []string {
	words := make([]string, n)
	for i := range words {
		words[i] = fmt.Sprintf("Customer%d Trading Industries Limited", i)
	}
	words[0], words[1] = "Acme Trading Co", "Foo Industries"
	return words
}

// TestWordListCostsTheTextOnce: a word list is matched whole, whatever its
// size — the words cost the text once, not once per word (#7227: 5415 words
// over a 3.6MB history cost 3.5s a turn, 49x the same text with two words;
// with the words matched in one walk, the same text costs the same).
// What is compared is the same call on the same text, the history's two
// names alone against the 5415-name list holding them, as the best of three
// runs each, so a busy machine moves both sides alike.
func TestWordListCostsTheTextOnce(t *testing.T) {
	text := strings.Repeat(wordsPara, 8000)
	all := wordList(5415)
	two := append([]string{}, all[:2]...)
	plain := Options{Words: two, Secrets: true, Personal: true}
	full := Options{Words: all, Secrets: true, Personal: true}
	Mask(text, plain) // the rules' memo holds, as the gateway's next turn has it
	for name, o := range map[string]Options{"two": plain, "all": full} {
		if _, n := Mask(text, o); n < 15000 {
			t.Fatalf("%s masked %d places; the history holds its two names 8000 times over", name, n)
		}
	}
	best := func(o Options) time.Duration {
		var low time.Duration
		for i := 0; i < 3; i++ {
			t0 := time.Now()
			Mask(text, o)
			if d := time.Since(t0); i == 0 || d < low {
				low = d
			}
		}
		return low
	}
	if plain, full := best(plain), best(full); full > 5*plain {
		t.Fatalf("5415 words cost %v where the same text costs %v with two of them", full, plain)
	}
}

// TestWordsWholeTakesTheLongerWordAtAPlace: where the user asked for whole
// words, a word that sits inside a longer one is passed over at that place,
// and the longer word is still taken there — "ABCD" holds "ABC" that is no
// whole word of its own, and masks as ABCD.
func TestWordsWholeTakesTheLongerWordAtAPlace(t *testing.T) {
	o := Options{Words: []string{"ABC", "ABCD"}, WordsWhole: true, Secrets: true}
	out, n := Mask(" ABCD x", o)
	if n != 1 || strings.Contains(out, "ABCD") {
		t.Fatalf("masked %d: %q", n, out)
	}
	o.WordsWhole = false
	out, n = Mask(" ABCD x", o)
	if n != 1 || strings.Contains(out, "ABC") {
		t.Fatalf("masked %d: %q", n, out)
	}
}

// BenchmarkMaskWordsList is the reporter's case (#7227): 5415 words over a
// 3.6MB history, the rules' memo warm as the gateway's is.
func BenchmarkMaskWordsList(b *testing.B) {
	text := strings.Repeat(wordsPara, 8000)
	o := Options{Words: wordList(5415), Secrets: true, Personal: true}
	Mask(text, o)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for i := 0; i < b.N; i++ {
		Mask(text, o)
	}
}

// BenchmarkMaskWordsAnyCase is the same with the words matched whatever
// their case, over a Cyrillic history of the same size.
func BenchmarkMaskWordsAnyCase(b *testing.B) {
	text := strings.Repeat(`Проверка сборки: тест TestClientPathCoversHandler упал, ключ только
для чтения попросил /magpie-ca.crt и получил 404. Заказчик Acme Trading Co
сказал, что счёт 2026-11 банк отверг; Foo Industries вернул его в тот же день. `, 8000)
	o := Options{Words: wordList(5415), WordsAnyCase: true, Secrets: true, Personal: true}
	Mask(text, o)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for i := 0; i < b.N; i++ {
		Mask(text, o)
	}
}
