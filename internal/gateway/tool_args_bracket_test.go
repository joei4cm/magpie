package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A model writes a tool call's arguments with a closing bracket of the
// wrong kind: the "]" that closes an array comes as a "}", or the other
// way round (yetone/magpie#7678, ArmedHelicopter's DeepSeek harness on
// Windows). The arguments reach the client as the model wrote them, its
// json.loads turns them away, and the turn ends with nothing said: the
// tool never ran, and the next turn starts from nothing.

// brokenArgs is the reporter's own arguments, the ones their client
// could not parse: the model closed the observations array with a "}"
// and the entities array with a "]".
const brokenArgs = `{"arguments": {"entities": [{"name": "全局工作规则", "entityType": "rule", "observations": ["填写或引用学术论文时，绝不凭借刻板印象、记忆或猜测填写论文题目、作者、期刊、年份与数据；必须先用联网检索或原文核实，确认无误后再写入文档。", "禁止使用 DeepSeek 官方搜索（其 web_search 走 DeepSeek API，HTTP 402 不可用且被用户禁用）；联网检索改用 firecrawl 或 magpie-web-search 等工具。", "使用 firecrawl 之前必须先拉起 Docker：启动 Docker Desktop 并使 firecrawl 本地服务（docker compose up）就绪后再调用 firecrawl 工具。"}]}]}`

// fixedArgs is the same arguments with the brackets the text's own
// structure says: the observations array closed with a "]", the entity
// with a "}", the entities array with a "]", and the two objects around
// them with a "}" each.
var fixedArgs = brokenArgs[:len(brokenArgs)-5] + `]}]}}`

// reporterPieces is the reporter's arguments in the pieces an upstream
// sends them in: the last five brackets are the ones that went wrong.
var reporterPieces = []string{
	brokenArgs[:len(brokenArgs)-5],
	`}]`,
	`}]}`,
}

// chatBrokenStream is the reporter's call from a Chat upstream: the
// arguments in the pieces an upstream sends them in.
func chatBrokenStream() string {
	return sse(
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"mcp__magpie__call_tool","arguments":`+quoted(reporterPieces[0])+`}}]}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":`+quoted(reporterPieces[1])+`}}]}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":`+quoted(reporterPieces[2])+`}}]}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
}

// chatWholeBroken is the same call in a whole Chat reply, the arguments
// in one piece.
const chatWholeBroken = `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"mcp__magpie__call_tool","arguments":"{\"arguments\": {\"entities\": [{\"name\": \"全局工作规则\", \"entityType\": \"rule\", \"observations\": [\"填写或引用学术论文时，绝不凭借刻板印象、记忆或猜测填写论文题目、作者、期刊、年份与数据；必须先用联网检索或原文核实，确认无误后再写入文档。\", \"禁止使用 DeepSeek 官方搜索（其 web_search 走 DeepSeek API，HTTP 402 不可用且被用户禁用）；联网检索改用 firecrawl 或 magpie-web-search 等工具。\", \"使用 firecrawl 之前必须先拉起 Docker：启动 Docker Desktop 并使 firecrawl 本地服务（docker compose up）就绪后再调用 firecrawl 工具。\"}]}]}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":3}}`

// messagesBrokenStream is the reporter's call from a Messages upstream:
// the input in the pieces an upstream sends them in.
func messagesBrokenStream() string {
	return sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"mcp__magpie__call_tool","input":{}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+quoted(reporterPieces[0])+`}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+quoted(reporterPieces[1])+`}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+quoted(reporterPieces[2])+`}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":1,"output_tokens":3}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
	)
}

// bracketChatPieces reads what a Chat client was given of a tool call's
// arguments: its pieces, in the order they came.
func bracketChatPieces(t *testing.T, body string) []string {
	t.Helper()
	var pieces []string
	for _, ev := range events(body) {
		choices, _ := ev["choices"].([]any)
		for _, c := range choices {
			ch, _ := c.(map[string]any)
			if ch == nil {
				continue
			}
			delta, _ := ch["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			calls, _ := delta["tool_calls"].([]any)
			for _, k := range calls {
				call, _ := k.(map[string]any)
				if call == nil {
					continue
				}
				fn, _ := call["function"].(map[string]any)
				if fn == nil {
					continue
				}
				if a, _ := fn["arguments"].(string); a != "" {
					pieces = append(pieces, a)
				}
			}
		}
	}
	return pieces
}

// bracketChatArgs reads what a Chat client was given of a tool call's
// arguments: its pieces, stitched.
func bracketChatArgs(t *testing.T, body string) string {
	t.Helper()
	return strings.Join(bracketChatPieces(t, body), "")
}

// bracketInput reads what a Messages client was given of a tool call's
// input: its partial_json pieces, stitched.
func bracketInput(t *testing.T, body string) string {
	t.Helper()
	var pieces []string
	for _, ev := range events(body) {
		if ev["type"] != "content_block_delta" {
			continue
		}
		d, _ := ev["delta"].(map[string]any)
		if d == nil || d["type"] != "input_json_delta" {
			continue
		}
		p, _ := d["partial_json"].(string)
		pieces = append(pieces, p)
	}
	return strings.Join(pieces, "")
}

// TestBracketTextFixesTheReportersArguments is the reporter's own
// sample: the arguments their client could not parse come out with the
// brackets the text's structure says.
func TestBracketTextFixesTheReportersArguments(t *testing.T) {
	if json.Valid([]byte(brokenArgs)) {
		t.Fatal("the fixture parses: it is not the broken one")
	}
	got := bracketText(brokenArgs)
	if got != fixedArgs {
		t.Fatalf("got %s, want %s", got, fixedArgs)
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("the fixed arguments do not parse: %s", got)
	}
}

// TestBracketTextPiecesMakeTheWhole is the same arguments in the pieces
// an upstream sends them in: the text they add up to is the text the
// whole would be corrected into.
func TestBracketTextPiecesMakeTheWhole(t *testing.T) {
	var b bracketArgs
	got := ""
	for _, p := range reporterPieces {
		got += b.feed(p)
	}
	if got != fixedArgs {
		t.Fatalf("got %s, want %s", got, fixedArgs)
	}
}

// TestBracketTextLeavesHealthyTextAlone says a healthy call's arguments
// come out as they came.
func TestBracketTextLeavesHealthyTextAlone(t *testing.T) {
	for _, s := range []string{
		`{"a":1,"b":[1,2,{"c":"}"}]}`,
		`{"a":"}{[]"}`,
		`{"a":{"b":{"c":[1,2,3]}}}`,
		`{}`,
		`[]`,
	} {
		if got := bracketText(s); got != s {
			t.Errorf("%s: got %s", s, got)
		}
	}
}

// TestBracketTextLeavesProseAlone says arguments that are no JSON at all
// come out as they came: a closing bracket in prose closes nothing, and
// the text says no bracket was opened.
func TestBracketTextLeavesProseAlone(t *testing.T) {
	for _, s := range []string{"hello } world", "a } b", "}{", "}"} {
		if got := bracketText(s); got != s {
			t.Errorf("%s: got %s", s, got)
		}
	}
}

// TestBracketTextDropsACloserThatClosesNothing says a closing bracket
// with nothing open under it goes: it is the extra one a model wrote.
func TestBracketTextDropsACloserThatClosesNothing(t *testing.T) {
	if got := bracketText(`{"a":1}}`); got != `{"a":1}` {
		t.Errorf("got %s, want %s", got, `{"a":1}`)
	}
}

// TestChatRelayedToolArgsGetTheirBracketsFixed is the route the issue
// came from: a Chat client reads a Chat upstream's stream as it came,
// and the arguments in it carry the brackets of the wrong kind.
func TestChatRelayedToolArgsGetTheirBracketsFixed(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: chatBrokenStream()}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/chat/completions",
		`{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := bracketChatArgs(t, body); got != fixedArgs {
		t.Fatalf("client read %s, want %s", got, fixedArgs)
	}
}

// TestChatWholeRelayedToolArgsGetTheirBracketsFixed is the same call in
// a whole reply, the arguments in one piece.
func TestChatWholeRelayedToolArgsGetTheirBracketsFixed(t *testing.T) {
	fresh(t)
	f := &fake{t: t, ctype: "application/json", reply: chatWholeBroken}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/chat/completions",
		`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var reply struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("reply: %s", body)
	}
	for _, c := range reply.Choices {
		for _, call := range c.Message.ToolCalls {
			if call.Function.Arguments != fixedArgs {
				t.Fatalf("client read %s, want %s", call.Function.Arguments, fixedArgs)
			}
			return
		}
	}
	t.Fatalf("no call in %s", body)
}

// TestMessagesRelayedToolArgsGetTheirBracketsFixed is the same call from
// a Messages upstream, the input in the pieces an upstream sends them in.
func TestMessagesRelayedToolArgsGetTheirBracketsFixed(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: messagesBrokenStream()}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1/messages",
		`{"model":"m1","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := bracketInput(t, body); got != fixedArgs {
		t.Fatalf("client read %s, want %s", got, fixedArgs)
	}
}

// TestTranslatedToolArgsGetTheirBracketsFixed is the same call from an
// upstream magpie speaks Chat to, translated for the Messages client.
func TestTranslatedToolArgsGetTheirBracketsFixed(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: chatBrokenStream()}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/messages",
		`{"model":"m1","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := bracketInput(t, body); got != fixedArgs {
		t.Fatalf("client read %s, want %s", got, fixedArgs)
	}
}

// TestTranslatedWholeToolArgsGetTheirBracketsFixed is the same call from
// an upstream magpie speaks Chat to, in one body for the Messages client.
func TestTranslatedWholeToolArgsGetTheirBracketsFixed(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: chatBrokenStream()}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/messages",
		`{"model":"m1","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var reply struct {
		Content []struct {
			Type  string          `json:"type"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal([]byte(body), &reply) != nil {
		t.Fatalf("reply: %s", body)
	}
	for _, block := range reply.Content {
		if block.Type == "tool_use" {
			want := compact(t, json.RawMessage(fixedArgs))
			if got := compact(t, block.Input); got != want {
				t.Fatalf("client read %s, want %s", got, want)
			}
			return
		}
	}
	t.Fatalf("no call in %s", body)
}

// TestChatRelayKeepsHealthyBrackets says a healthy call's arguments go
// to the client as they came.
func TestChatRelayKeepsHealthyBrackets(t *testing.T) {
	fresh(t)
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Write","arguments":"{\"a\":1,\"b\":[1,2]}"}}]}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/chat/completions",
		`{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := bracketChatArgs(t, body); got != `{"a":1,"b":[1,2]}` {
		t.Fatalf("client read %s", got)
	}
}
