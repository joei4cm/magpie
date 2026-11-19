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

// #2878 (LUDA0831, v0.1.3871): the plugin since plugins#454 tells the
// pools spent in turn as windows set aside beside their Total credits, so
// a card reads Total credits with room beside a dedicated package used up
// (act-20261007-561, 1000 / 1000, a campaign's Qwen credits). The package
// drowned the Total pool in For — the trace said used 100, amount 1000,
// limit 1000 — and Smart kept the account for last while Qoder answered
// on its Total credits. The plugin tells no span, so both pools are the
// same (none) long and the used-up one is no share used.
func TestAUsedUpDedicatedPackageIsNoShareUsed(t *testing.T) {
	now := time.Now()
	until := now.Add(11 * 24 * time.Hour).UTC().Format(time.RFC3339)
	card := func(totalUsed, totalLimit float64, totalDisplay string) Allowance {
		// the Qoder plugin's usage answer (packages/qoder/index.mjs,
		// parseUsage): Total credits first, the pools spent in turn set
		// aside, the dedicated package after
		return PluginAllowanceForTest([]byte(`{"plan":"personal_professional","windows":[`+
			`{"name":"Total credits","used":`+jsonNum(100*totalUsed/totalLimit)+`,"amount":`+jsonNum(totalUsed)+`,"limit":`+jsonNum(totalLimit)+`,"unit":"credits","resetsAt":"`+until+`","display":"`+totalDisplay+`"},`+
			`{"name":"Credits","used":37.1,"amount":742,"limit":2000,"unit":"credits","resetsAt":"`+until+`","display":"742 / 2,000 credits","aside":true},`+
			`{"name":"Add-on credits","used":33.333333,"amount":1000,"limit":3000,"unit":"credits","display":"1,000 / 3,000 credits","aside":true},`+
			`{"name":"act-20261007-561","used":100,"amount":1000,"limit":1000,"unit":"credits","display":"1,000 / 1,000 credits"}]}`), now)
	}
	near := func(used, want float64) bool { return used > want-0.01 && used < want+0.01 }

	// the reporter's card: the dedicated package used up, Total credits
	// (1742 / 5000) with room
	if used, _ := card(1742, 5000, "1,742 / 5,000 credits").For("dfmodel", now); !near(used, 34.84) {
		t.Fatalf("dedicated package used up, Total credits left: %v%% used, want the Total pool's 34.84", used)
	}
	// the Total pool used up too: out as before
	if used, _ := card(5000, 5000, "5,000 / 5,000 credits").For("dfmodel", now); !near(used, 100) {
		t.Fatalf("dedicated package and Total credits both used up: %v%% used, want 100", used)
	}
}
