package gateway

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #2878 (gtk96): a Qoder account whose Credits window is used up (300 / 300)
// still has its Add-on credits (400 / 900) and Qoder answers on them. Smart
// weighed the member by that full window alone and kept it for last, so a
// group whose other member always answered never asked it, first in the
// group or not.
func TestGroupTriesAQoderAccountOnItsAddOnCredits(t *testing.T) {
	forgetRouting()
	now := time.Now()
	old, oldKey := allowances, keyAllowance
	t.Cleanup(func() { allowances, keyAllowance = old, oldKey })
	keyAllowance = func(provider.Provider) (provider.Allowance, bool) { return nil, false }
	// the Qoder plugin's usage answer, as packages/qoder/index.mjs builds it
	card := func(addonUsed, addonDisplay string) provider.Allowance {
		return provider.PluginAllowanceForTest([]byte(`{"windows":[`+
			`{"name":"Credits","used":100,"display":"300 / 300 credits"},`+
			`{"name":"Add-on credits","used":`+addonUsed+`,"display":"`+addonDisplay+`"}]}`), now)
	}
	qoder := candidate{p: provider.Provider{ID: "qoder-cn", Account: &provider.Account{Agent: "plugin:qoder-cn", User: "me@x.com"}}, model: "dfmodel", rest: "qoder-cn"}
	// the group's other member: a plain key, its windows read by nobody, so
	// it weighs as fresh and answers whatever it is asked
	dsh := candidate{p: provider.Provider{ID: "dsh-fe", Key: "sk-dsh", Chat: "http://192.168.4.236:3001"}, model: "deepseek-v4-flash", rest: "dsh-fe"}
	group := provider.Provider{ID: "group/auto-deepseek-v4-1-flash"}

	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"me@x.com": card("44.444444", "400 / 900 credits")}
	}
	got, wg := weigh(group, []candidate{qoder, dsh}, "dfmodel", provider.Chat)
	if got[0].rest != "qoder-cn" {
		t.Fatalf("Credits used up, Add-on credits left: the group tries %s, want qoder-cn first", restsOf(got))
	}
	if l := wg.lefts[qoder.allowanceKey()]; l.used < 44 || l.used > 45 {
		t.Fatalf("the account is weighed at %v%% used, want the Add-on pool's 44.4", l.used)
	}

	// both pools used up: kept for when nothing else can take a request
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"me@x.com": card("100", "900 / 900 credits")}
	}
	if got, _ := weigh(group, []candidate{qoder, dsh}, "dfmodel", provider.Chat); got[0].rest != "dsh-fe" {
		t.Fatalf("both pools used up: the group tries %s, want the fresh key first", restsOf(got))
	}
}

// #2878 (LUDA0831, v0.1.3871, the plugin since plugins#454): the card tells
// the pools spent in turn beside their Total credits, set aside, and a
// dedicated campaign package (act-20261007-561, 1000 / 1000) that counts
// every model. Weighed by that used-up package alone the member went to
// Smart's spent queue and a group whose other member always answered never
// asked it, though Qoder serves the model on its Total credits (1742 /
// 5000): the reporter's trace said used 100, amount 1000, limit 1000.
func TestGroupTriesAQoderAccountOnItsTotalCredits(t *testing.T) {
	forgetRouting()
	now := time.Now()
	old, oldKey := allowances, keyAllowance
	t.Cleanup(func() { allowances, keyAllowance = old, oldKey })
	keyAllowance = func(provider.Provider) (provider.Allowance, bool) { return nil, false }
	until := now.Add(11 * 24 * time.Hour).UTC().Format(time.RFC3339)
	// the Qoder plugin's usage answer, as packages/qoder/index.mjs builds
	// it since plugins#454
	card := func(totalShare, totalUsed, totalLimit, totalDisplay string) provider.Allowance {
		return provider.PluginAllowanceForTest([]byte(`{"windows":[`+
			`{"name":"Total credits","used":`+totalShare+`,"amount":`+totalUsed+`,"limit":`+totalLimit+`,"unit":"credits","resetsAt":"`+until+`","display":"`+totalDisplay+`"},`+
			`{"name":"Credits","used":37.1,"amount":742,"limit":2000,"unit":"credits","resetsAt":"`+until+`","display":"742 / 2,000 credits","aside":true},`+
			`{"name":"Add-on credits","used":33.333333,"amount":1000,"limit":3000,"unit":"credits","display":"1,000 / 3,000 credits","aside":true},`+
			`{"name":"act-20261007-561","used":100,"amount":1000,"limit":1000,"unit":"credits","display":"1,000 / 1,000 credits"}]}`), now)
	}
	qoder := candidate{p: provider.Provider{ID: "qoder-cn", Account: &provider.Account{Agent: "plugin:qoder-cn", User: "me@x.com"}}, model: "dfmodel", rest: "qoder-cn"}
	// the group's other member: a plain key that always answers
	dsh := candidate{p: provider.Provider{ID: "dsh-fe", Key: "sk-dsh", Chat: "http://192.168.4.236:3001"}, model: "deepseek-v4-flash", rest: "dsh-fe"}
	group := provider.Provider{ID: "group/auto-deepseek-v4-1-flash"}

	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"me@x.com": card("34.84", "1742", "5000", "1,742 / 5,000 credits")}
	}
	got, wg := weigh(group, []candidate{qoder, dsh}, "dfmodel", provider.Chat)
	if got[0].rest != "qoder-cn" {
		t.Fatalf("dedicated package used up, Total credits left: the group tries %s, want qoder-cn first", restsOf(got))
	}
	if l := wg.lefts[qoder.allowanceKey()]; l.used < 34 || l.used > 35 {
		t.Fatalf("the account is weighed at %v%% used, want the Total pool's 34.84", l.used)
	}

	// the Total pool used up too: kept for when nothing else can take a request
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"me@x.com": card("100", "5000", "5000", "5,000 / 5,000 credits")}
	}
	if got, _ := weigh(group, []candidate{qoder, dsh}, "dfmodel", provider.Chat); got[0].rest != "dsh-fe" {
		t.Fatalf("dedicated package and Total credits both used up: the group tries %s, want the fresh key first", restsOf(got))
	}
}
