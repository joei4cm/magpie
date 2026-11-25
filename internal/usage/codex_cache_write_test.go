package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// #589: a Codex call through the gateway whose upstream told a cache write
// was listed twice, once under its provider and once more as a local session
// call: Codex's rollout says the write (cache_write_input_tokens) and the
// gateway's record didn't, so their token counts never matched. However each
// side splits the write out of the input, a call is the gateway's alone.
func TestCodexCacheWriteMatchesGatewayCall(t *testing.T) {
	end := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)).Add(-time.Minute)
	// the upstream's usage for the two calls: the second wrote 9000 to its cache
	type call struct{ in, cached, write, out int }
	calls := []call{{120000, 113000, 0, 300}, {130000, 113000, 9000, 400}}
	for _, c := range []struct {
		name       string
		codexWrite bool // Codex's rollout names cache writes (newer Codex)
		splitWrite bool // the gateway's record splits them out of the input
	}{
		{"codex says the write, gateway didn't", true, false},
		{"both say the write", true, true},
		{"older codex, gateway says the write", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			pageHome(t)
			path := filepath.Join(sessions.CodexDir(), "sessions", "rollout-2026-09-30T00-00-00-s589.jsonl")
			os.MkdirAll(filepath.Dir(path), 0700)
			var b strings.Builder
			b.WriteString(`{"type":"session_meta","payload":{"id":"s589","model_provider":"magpie"}}` + "\n")
			b.WriteString(`{"type":"turn_context","payload":{"model":"maru-code/gpt-6.1-sol","effort":"high"}}` + "\n")
			usageOf := func(in, cached, write, out int) string {
				if !c.codexWrite {
					return fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}`, in, cached, out)
				}
				return fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":%d,"output_tokens":%d}`, in, cached, write, out)
			}
			var tot call
			var recs []string
			for i, k := range calls {
				at := end.Add(time.Duration(i-len(calls)) * time.Minute)
				tot.in, tot.cached, tot.write, tot.out = tot.in+k.in, tot.cached+k.cached, tot.write+k.write, tot.out+k.out
				fmt.Fprintf(&b, `{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":%s,"last_token_usage":%s}}}`+"\n",
					at.Format(time.RFC3339Nano), usageOf(tot.in, tot.cached, tot.write, tot.out), usageOf(k.in, k.cached, k.write, k.out))
				r := Record{Time: at.Add(-5 * time.Second), Millis: 5000, Agent: "codex", Provider: "maru-code", Host: "api.muteki.site",
					Model: "gpt-6.1-sol", Requested: "maru-code/gpt-6.1-sol", Served: "gpt-6.1-sol", Effort: "high", Status: 200,
					Input: k.in - k.cached, Output: k.out, CacheRead: k.cached, Session: "s589", NativeSession: "s589", Endpoint: "/v1/responses"}
				if c.splitWrite {
					r.Input, r.CacheWrite = k.in-k.cached-k.write, k.write
				}
				j, _ := json.Marshal(r)
				recs = append(recs, string(j))
			}
			os.WriteFile(path, []byte(b.String()), 0600)
			os.MkdirAll(filepath.Dir(Path()), 0700)
			os.WriteFile(Path(), []byte(strings.Join(recs, "\n")+"\n"), 0600)

			for _, f := range []Filter{{}, {Provider: "maru-code"}} {
				p := QueryPage(Today, f, 0, 100)
				if p.Total != 2 || p.Sum.Calls != 2 {
					t.Fatalf("filter %+v: %d rows, %d calls, want the gateway's 2: %+v", f, p.Total, p.Sum.Calls, p.Rows)
				}
				for _, r := range p.Rows {
					if r.Source == "log" {
						t.Fatalf("filter %+v: the gateway's call is listed again from Codex's log: %+v", f, r)
					}
				}
				if by := p.By["provider"]; len(by) != 1 || by[0].ID != "maru-code" || by[0].Calls != 2 {
					t.Fatalf("filter %+v: provider ranking %+v, want maru-code alone", f, by)
				}
				l := LedgerOf(Today, f)
				if len(l.Rows) != 2 || l.Sum.Calls != 2 {
					t.Fatalf("filter %+v: ledger has %d rows: %+v", f, len(l.Rows), l.Rows)
				}
			}
		})
	}
}

// Codex's input_tokens holds what was written to the cache as well as what
// was read from it; a call's input is what was neither.
func TestCodexCacheWriteNotInInput(t *testing.T) {
	pageHome(t)
	path := filepath.Join(sessions.CodexDir(), "sessions", "rollout-2026-09-30T00-00-00-w.jsonl")
	os.MkdirAll(filepath.Dir(path), 0700)
	at := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)).Add(-time.Minute).Format(time.RFC3339Nano)
	u := `{"input_tokens":130000,"cached_input_tokens":113000,"cache_write_input_tokens":9000,"output_tokens":400}`
	os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"w"}}`+"\n"+
		`{"timestamp":"`+at+`","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":`+u+`,"last_token_usage":`+u+`}}}`+"\n"), 0600)
	p := QueryPage(Today, Filter{}, 0, 10)
	if len(p.Rows) != 1 || p.Rows[0].Input != 8000 || p.Rows[0].CacheRead != 113000 || p.Rows[0].CacheWrite != 9000 {
		t.Fatalf("rows %+v", p.Rows)
	}
}

// #7746: a Codex call driven by a client whose own agent is not codex —
// T3 Code running codex app-server — is recorded on the gateway side
// under that agent, while the session file of the same call is always
// Codex's. The response id names only a Codex answer, so the two sides
// pair by it whatever the gateway's client called itself; holding the
// gateway row to Agent == "codex" left the call as two rows.
func TestCodexClientOtherThanCodexMatchesByResponseID(t *testing.T) {
	pageHome(t)
	end := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)).Add(-time.Minute)
	path := filepath.Join(sessions.CodexDir(), "sessions", "rollout-2026-09-30T00-00-00-t3.jsonl")
	os.MkdirAll(filepath.Dir(path), 0700)
	u := `{"input_tokens":120000,"cached_input_tokens":113000,"output_tokens":300}`
	// the same response id as the gateway's record names
	var b strings.Builder
	b.WriteString(`{"type":"session_meta","payload":{"id":"t3","model_provider":"openai"}}` + "\n")
	b.WriteString(`{"type":"turn_context","payload":{"model":"gpt-6.2-sol","effort":"high"}}` + "\n")
	// a token_usage_record carries the call's response id; the gateway's
	// record of the same call names it too
	fmt.Fprintf(&b, `{"timestamp":%q,"type":"token_usage_record","payload":{"response_id":"resp-t3","session_id":"t3","thread_id":"t3","turn_id":"turn","usage":%s}}`+"\n",
		end.Format(time.RFC3339Nano), u)
	os.WriteFile(path, []byte(b.String()), 0600)

	// the gateway's record of the same call, under the client's own agent:
	// no Session, so only the response id can pair it with the session file
	rec := Record{Time: end.Add(-5 * time.Second), Millis: 5000, Agent: "t3code", Provider: "maru-code", Host: "api.muteki.site",
		Model: "gpt-6.2-sol", Requested: "group/group", Served: "gpt-6.2-sol", Effort: "high", Status: 200,
		Input: 7000, Output: 300, CacheRead: 113000, ResponseID: "resp-t3", Endpoint: "/v1/responses"}
	j, _ := json.Marshal(rec)
	os.MkdirAll(filepath.Dir(Path()), 0700)
	os.WriteFile(Path(), []byte(string(j)+"\n"), 0600)

	p := QueryPage(Today, Filter{}, 0, 100)
	if p.Sum.Calls != 1 {
		t.Fatalf("the call is listed %d times, want once: %+v", p.Sum.Calls, p.Rows)
	}
	for _, r := range p.Rows {
		if r.Source == "log" {
			t.Fatalf("the gateway's call is listed again from Codex's session file: %+v", r)
		}
	}
}
