package provider

import (
	"testing"
	"time"
)

// #2878 (gtk96): a Qoder account whose Credits window is used up still has
// its Add-on credits, and Qoder answers on them (the reporter's card:
// Credits 300 / 300, Add-on credits 400 / 900). Routing weighed the card by
// that full window alone, so Smart kept the account for last and a group
// whose other member answered never sent it a request.
func TestACreditsPoolUsedUpIsNoShareUsed(t *testing.T) {
	now := time.Now()
	// the Qoder plugin's usage answer (packages/qoder/index.mjs, parseUsage):
	// one window per pool, a share and the pool's own count
	card := func(credits, addon float64) Allowance {
		return PluginAllowanceForTest([]byte(`{"plan":"personal_professional","windows":[`+
			`{"name":"Credits","used":`+jsonNum(credits)+`,"display":"`+jsonNum(credits*3)+` / 300 credits"},`+
			`{"name":"Add-on credits","used":`+jsonNum(addon)+`,"display":"`+jsonNum(addon*9)+` / 900 credits"}]}`), now)
	}
	near := func(used, want float64) bool { return used > want-0.01 && used < want+0.01 }

	// the check-in's Credits used up, Add-on credits with room
	if used, _ := card(100, 44.444444).For("qmodel_38max", now); !near(used, 44.444444) {
		t.Fatalf("Credits used up, Add-on credits left: %v%% used, want the Add-on pool's 44.4", used)
	}
	// the other way round, and both used up: out as before
	if used, _ := card(0, 100).For("qmodel_38max", now); !near(used, 0) {
		t.Fatalf("Add-on credits used up, Credits left: %v%% used", used)
	}
	if used, _ := card(100, 100).For("qmodel_38max", now); !near(used, 100) {
		t.Fatalf("both pools used up: %v%% used, want 100", used)
	}
	// both read used up, the Add-on pool's reset passed since: empty again
	reset := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	if used, _ := PluginAllowanceForTest([]byte(`{"windows":[`+
		`{"name":"Credits","used":100,"display":"300 / 300 credits"},`+
		`{"name":"Add-on credits","used":100,"resetsAt":"`+reset+`","display":"900 / 900 credits"}]}`), now).For("qmodel_38max", now); !near(used, 0) {
		t.Fatalf("Add-on credits started again while Credits is used up: %v%% used, want 0", used)
	}

	// a card's windows that aren't credit pools are limits over each other,
	// not buckets: percentages renewing alike, and credits over two spans
	// (Open Design's five hours and its week), are still the fullest's
	limits := func(ws string) Allowance {
		return PluginAllowanceForTest([]byte(`{"windows":[`+ws+`]}`), now)
	}
	share := limits(`{"name":"5 hours","used":100,"span":18000,"display":"100% used"},` +
		`{"name":"7 days","used":40,"span":604800,"display":"40% used"}`)
	if used, _ := share.For("gpt-6-astra", now); !near(used, 100) {
		t.Fatalf("one of two percent windows used up: %v%% used, want 100", used)
	}
	spans := limits(`{"name":"5 hours","used":100,"span":18000,"display":"5 / 5 credits"},` +
		`{"name":"Week","used":40,"span":604800,"display":"400 / 1000 credits"}`)
	if used, _ := spans.For("kimi-for-coding", now); !near(used, 100) {
		t.Fatalf("one allowance over two spans, the short one used up: %v%% used, want 100", used)
	}
}
