package redact

import (
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// A user's word list is matched whole: one walk over a text finds every
// word of it in it, whatever the list's size. Each word used to be looked
// for on its own — a strings.Index per word, a case-blind regexp where the
// user asked for that — so a request paid the whole history once per word:
// with 5415 words (a customer and vendor list) and a 3.6MB history, the
// words alone cost 3.5 seconds on every turn, while the same text with two
// words cost 71 milliseconds (#7227). The walk is the text once, whatever
// it holds.

// wordNode is a place in the words as they are written: a byte of a word
// apiece. term says a word ends here.
type wordNode struct {
	kids map[byte]*wordNode
	term bool
}

// foldNode is a place in the words matched whatever their case: a rune of
// a word apiece, folded (foldRune). Two runes sit at the same place when
// (?i) matches them, as the case-blind words always did.
type foldNode struct {
	kids map[rune]*foldNode
	term bool
}

// wordTrie is the user's words, ready for one walk: the date words, which
// match as the date they are wherever it is written (dateword.go), and the
// rest as a trie — bytes as written, or folded runes where the words are
// matched whatever their case.
type wordTrie struct {
	dates []string
	// root holds the first byte of every word; the bytes no word starts
	// with are passed over for the price of an array read
	root *[256]*wordNode
	fold map[rune]*foldNode
}

// foldRune is the lowest rune of r's case-folding orbit: two runes match
// (?i) — as the words are matched case-blind — when their orbits hold the
// same runes, and so when this is the same rune for both.
func foldRune(r rune) rune {
	if r < 0x80 {
		if r >= 'a' && r <= 'z' {
			return r - 32
		}
		return r // a digit, a mark: its orbit is itself
	}
	min := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < min {
			min = f
		}
	}
	return min
}

// wordSetFor keeps the last list's set. The list is read again from the
// settings on every request, and every text of a request is masked on its
// own; building the trie is the list walked once, and is not paid again
// while the list is the same.
var wordSetFor struct {
	sync.Mutex
	words   []string
	anyCase bool
	set     *wordTrie
}

// wordsOf is the set of the user's words as o masks them.
func wordsOf(o Options) *wordTrie {
	wordSetFor.Lock()
	defer wordSetFor.Unlock()
	if wordSetFor.set != nil && wordSetFor.anyCase == o.WordsAnyCase && slices.Equal(wordSetFor.words, o.Words) {
		return wordSetFor.set
	}
	set := &wordTrie{}
	for _, w := range o.Words {
		if w = strings.TrimSpace(w); len(w) < 2 {
			continue // as the words were taken before: one letter alone is not one
		}
		if dateWord(w) != nil {
			set.dates = append(set.dates, w)
			continue // a date is matched as that date, however it is written
		}
		if o.WordsAnyCase {
			set.fold = putFold(set.fold, w)
		} else {
			set.root = putBytes(set.root, w)
		}
	}
	wordSetFor.words, wordSetFor.anyCase, wordSetFor.set = o.Words, o.WordsAnyCase, set
	return set
}

// putBytes adds w to the trie of words as they are written.
func putBytes(root *[256]*wordNode, w string) *[256]*wordNode {
	if root == nil {
		root = &[256]*wordNode{}
	}
	n := root[w[0]]
	if n == nil {
		n = &wordNode{}
		root[w[0]] = n
	}
	for i := 1; i < len(w); i++ {
		if n.kids == nil {
			n.kids = map[byte]*wordNode{}
		}
		k := n.kids[w[i]]
		if k == nil {
			k = &wordNode{}
			n.kids[w[i]] = k
		}
		n = k
	}
	n.term = true
	return root
}

// putFold adds w to the trie of words matched whatever their case.
func putFold(root map[rune]*foldNode, w string) map[rune]*foldNode {
	if root == nil {
		root = map[rune]*foldNode{}
	}
	var n *foldNode
	for i, r := range w {
		f := foldRune(r)
		kids := root
		if i > 0 {
			kids = n.kids
		}
		if kids == nil {
			kids = map[rune]*foldNode{}
			if i == 0 {
				root = kids
			} else {
				n.kids = kids
			}
		}
		k := kids[f]
		if k == nil {
			k = &foldNode{}
			kids[f] = k
		}
		n = k
	}
	n.term = true
	return root
}

// wordSpans is where s holds one of the user's words o masks, as one walk
// over it whatever the list's size.
func wordSpans(s string, o Options) []span {
	set := wordsOf(o)
	var out []span
	for _, w := range set.dates {
		if spans, ok := dateSpans(s, w); ok {
			out = append(out, spans...)
		}
	}
	if set.fold != nil {
		out = append(out, set.findFold(s, o.WordsWhole)...)
	} else if set.root != nil {
		out = append(out, set.findBytes(s, o.WordsWhole)...)
	}
	return out
}

// findBytes walks s a byte at a time and tells where a word of the trie,
// as it is written, is placed. Where a word is passed over because it sits
// inside a longer one (the user asked for whole words), a longer word at
// the same place is still tried: "XABCDEF" with ABC and ABCD asks for the
// one that is whole there.
func (ws *wordTrie) findBytes(s string, whole bool) []span {
	var out []span
	for i := 0; i < len(s); i++ {
		n := ws.root[s[i]]
		if n == nil {
			continue // no word starts with this byte
		}
		for j := i + 1; ; j++ { // j: one past the place held
			if n.term && (!whole || wholeWord(s, i, j)) {
				out = append(out, span{i, j, "TERM", ByWord, 0, false})
			}
			if j >= len(s) {
				break
			}
			if k := n.kids[s[j]]; k == nil {
				break
			} else {
				n = k
			}
		}
	}
	return out
}

// findFold walks s a rune at a time and tells where a word of the folded
// trie is placed, whatever its case. A rune of the text that is no start
// of a word costs a map read; whole words are asked of each place, as
// findBytes asks of it.
func (ws *wordTrie) findFold(s string, whole bool) []span {
	var out []span
	for i := 0; i < len(s); {
		r, wide := utf8.DecodeRuneInString(s[i:])
		n := ws.fold[foldRune(r)]
		j := i + wide
		for n != nil {
			if n.term && (!whole || wholeWord(s, i, j)) {
				out = append(out, span{i, j, "TERM", ByWord, 0, false})
			}
			if j >= len(s) {
				break
			}
			r2, w2 := utf8.DecodeRuneInString(s[j:])
			j += w2
			n = n.kids[foldRune(r2)]
		}
		i += wide
	}
	return out
}
