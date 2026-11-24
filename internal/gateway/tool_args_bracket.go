package gateway

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/yetone/magpie/internal/provider"
)

// A model writes a tool call's arguments with a closing bracket of the
// wrong kind: the "]" that closes an array comes as a "}", or the other
// way round (yetone/magpie#7678, ArmedHelicopter's DeepSeek harness on
// Windows: "Expected ',' or ']' after array element in JSON at position
// 375"). The arguments reach the client as the model wrote them, its
// json.loads turns them away, and the turn ends with nothing said and
// nothing to go on: the tool never ran, and the next turn starts from
// nothing.
//
// The text's own brackets say what each closing one should be: a stack
// holds the ones still open, and a closing bracket that does not match
// the one on top of the stack is corrected to the kind that does. So
// the reporter's own sample -- the model closed the observations array
// with a "}" and the entities array with a "]" -- comes out with the
// nesting its fields say, and nothing of the arguments is lost.
//
// The rule reads the text as JSON, so a bracket inside a string is text
// and not a bracket, and a text that is no JSON at all (a model that
// writes its arguments as prose) is left as it came.
//
// Arguments that were cut short are not completed here: the closers a
// cut text misses are what the model would have said, and #3189 asks
// for no such guessing.

// bracketArgs is a call's arguments text with its closing brackets
// corrected, fed to it a piece at a time. A piece waits for nothing: a
// closing bracket is corrected by the stack alone, so the text can be
// corrected as it comes.
type bracketArgs struct {
	stack  []byte // the brackets still open, '{' or '['
	str    bool   // in a string
	esc    bool   // in a string, after its backslash
	opened bool   // the text has an opening bracket: it is JSON, not prose
}

// feed is the text of a piece of a call's arguments, with its closing
// brackets corrected. A piece with none to correct goes as it came.
func (b *bracketArgs) feed(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case b.str:
			switch {
			case b.esc:
				b.esc = false
			case c == '\\':
				b.esc = true
			case c == '"':
				b.str = false
			}
		case c == '"':
			b.str = true
		case c == '{' || c == '[':
			b.opened = true
			b.stack = append(b.stack, c)
		case c == '}' || c == ']':
			if len(b.stack) == 0 {
				if !b.opened {
					break // prose: it goes as it came
				}
				// a closer that closes nothing goes
				if out == nil {
					out = []byte(s[:i])
				}
				continue
			}
			top := b.stack[len(b.stack)-1]
			b.stack = b.stack[:len(b.stack)-1]
			if top != c {
				// the stack says what this closer should be
				if out == nil {
					out = []byte(s[:i])
				}
				out = append(out, closerOf(top))
				continue
			}
		}
		if out != nil {
			out = append(out, c)
		}
	}
	if out == nil {
		return s
	}
	return string(out)
}

// closerOf is the closing bracket the open one o takes.
func closerOf(o byte) byte {
	if o == '{' {
		return '}'
	}
	return ']'
}

// bracketText is a call's whole arguments text with its closing
// brackets corrected.
func bracketText(s string) string {
	var b bracketArgs
	return b.feed(s)
}

// bracketEncoder hands its encoder a reply with the closing brackets of
// its calls' arguments corrected, streamed in pieces.
type bracketEncoder struct {
	streamEncoder
	args bracketArgs
}

func (e *bracketEncoder) event(ev Event) {
	switch ev.Kind {
	case KToolStart:
		e.args = bracketArgs{}
	case KToolArgs:
		ev.Text = e.args.feed(ev.Text)
	}
	e.streamEncoder.event(ev)
}

// bracketResult is a whole reply with the closing brackets of its
// calls' arguments corrected.
func bracketResult(res Result) Result {
	var parts []Part
	for i, p := range res.Parts {
		if p.Kind != ToolCall || len(p.Args) == 0 {
			continue
		}
		if a := bracketText(string(p.Args)); a != string(p.Args) {
			if parts == nil {
				parts = append([]Part(nil), res.Parts...)
			}
			parts[i].Args = json.RawMessage(a)
		}
	}
	if parts != nil {
		res.Parts = parts
	}
	return res
}

// bracketTidy corrects, in a reply relayed as it came, the closing
// brackets of its calls' arguments: Responses' function_call items and
// argument deltas, and Chat's tool_calls. A stream's lines pass as they
// came unless one carries arguments that change; a reply that isn't a
// stream is held whole and gone through at the end.
type bracketTidy struct {
	proto provider.Protocol
	sse   bool
	buf   []byte
	calls map[string]*bracketArgs // a streamed call's arguments so far, by its index or item id
}

func (t *bracketTidy) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	if !t.sse {
		return nil
	}
	i := bytes.LastIndexByte(t.buf, '\n')
	if i < 0 {
		return nil
	}
	out := t.lines(t.buf[:i+1])
	t.buf = append(t.buf[:0], t.buf[i+1:]...)
	return out
}

func (t *bracketTidy) flush() []byte {
	b := t.buf
	t.buf = nil
	if !t.sse {
		// every model's whole reply comes here now: one with no call in
		// it is said as it came, without being decoded
		if !t.carries(b) {
			return b
		}
		if out, ok := t.event(b); ok {
			return out
		}
		return b
	}
	return t.lines(b)
}

// carries is whether b may carry a call's arguments.
func (t *bracketTidy) carries(b []byte) bool {
	switch t.proto {
	case provider.Responses:
		return bodyHas(b, `function_call`)
	case provider.Chat:
		return bodyHas(b, `"tool_calls"`)
	case provider.Anthropic:
		// a whole reply's tool_use input is an object, which parses or the
		// reply does not; a stream's input is text, and may be broken
		return bodyHas(b, `"input_json_delta"`)
	}
	return false
}

func (t *bracketTidy) lines(b []byte) []byte {
	if !t.carries(b) {
		return append([]byte(nil), b...)
	}
	var out []byte
	for len(b) > 0 {
		line := b
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i+1], b[i+1:]
		} else {
			b = nil
		}
		body := bytes.TrimRight(line, "\r\n")
		data, ok := bytes.CutPrefix(body, []byte("data:"))
		if !ok || !t.carries(data) {
			out = append(out, line...)
			continue
		}
		nb, ok := t.event(data)
		if !ok {
			out = append(out, line...)
			continue
		}
		out = append(append(append(out, "data: "...), nb...), line[len(body):]...)
	}
	return out
}

// event is an event, or a whole reply, with the closing brackets of its
// calls' arguments corrected, and whether that changed it.
func (t *bracketTidy) event(data []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // the event's own numbers as they came
	var ev map[string]any
	if dec.Decode(&ev) != nil || ev == nil {
		return nil, false
	}
	var changed bool
	switch t.proto {
	case provider.Responses:
		changed = t.responses(ev)
	case provider.Chat:
		changed = t.chat(ev)
	case provider.Anthropic:
		changed = t.anthropic(ev)
	}
	if !changed {
		return nil, false
	}
	nb, err := marshalPlain(ev)
	if err != nil {
		return nil, false
	}
	return nb, true
}

// stream is the arguments so far of the streamed call at key.
func (t *bracketTidy) stream(key string) *bracketArgs {
	if t.calls == nil {
		t.calls = map[string]*bracketArgs{}
	}
	a := t.calls[key]
	if a == nil {
		a = &bracketArgs{}
		t.calls[key] = a
	}
	return a
}

func (t *bracketTidy) responses(ev map[string]any) bool {
	changed := false
	// a call said whole: its arguments are JSON text
	item := func(it map[string]any) {
		if it != nil && it["type"] == "function_call" && set(it, "arguments", bracketText) {
			changed = true
		}
	}
	it, _ := ev["item"].(map[string]any)
	item(it)
	switch ev["type"] {
	case "response.function_call_arguments.delta":
		key := callKeyOf(ev)
		if set(ev, "delta", t.stream(key).feed) {
			changed = true
		}
	case "response.function_call_arguments.done":
		delete(t.calls, callKeyOf(ev))
		if set(ev, "arguments", bracketText) {
			changed = true
		}
	}
	res, _ := ev["response"].(map[string]any)
	if res == nil && ev["object"] == "response" {
		res = ev // a whole reply
	}
	if res != nil {
		out, _ := res["output"].([]any)
		for _, o := range out {
			im, _ := o.(map[string]any)
			item(im)
		}
	}
	return changed
}

func (t *bracketTidy) chat(ev map[string]any) bool {
	changed := false
	choices, _ := ev["choices"].([]any)
	for _, c := range choices {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		// a stream's delta, its arguments in pieces by the call's index
		if d, _ := cm["delta"].(map[string]any); d != nil {
			calls, _ := d["tool_calls"].([]any)
			for j, tc := range calls {
				tm, _ := tc.(map[string]any)
				fn, _ := tm["function"].(map[string]any)
				if fn == nil {
					continue
				}
				idx := jsonText(tm["index"])
				if tm["index"] == nil {
					idx = strconv.Itoa(j)
				}
				key := jsonText(cm["index"]) + "/" + idx
				a := t.stream(key)
				if set(fn, "arguments", a.feed) {
					changed = true
				}
			}
		}
		// a whole reply's message
		if m, _ := cm["message"].(map[string]any); m != nil {
			calls, _ := m["tool_calls"].([]any)
			for _, tc := range calls {
				tm, _ := tc.(map[string]any)
				if fn, _ := tm["function"].(map[string]any); fn != nil && set(fn, "arguments", bracketText) {
					changed = true
				}
			}
		}
	}
	return changed
}

func (t *bracketTidy) anthropic(ev map[string]any) bool {
	// a stream's input, in pieces by the block's index
	if ev["type"] == "content_block_delta" {
		if d, _ := ev["delta"].(map[string]any); d != nil && d["type"] == "input_json_delta" {
			return set(d, "partial_json", t.stream(jsonText(ev["index"])).feed)
		}
	}
	return false
}
