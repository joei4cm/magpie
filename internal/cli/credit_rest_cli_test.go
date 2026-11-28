package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestCreditRestCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "credit-rest-cli", Name: "Credit rest", Chat: "https://example.invalid/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := creditRestCmd([]string{"credit-rest-cli", "24h"}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("credit-rest-cli")
	if err != nil {
		t.Fatal(err)
	}
	if p.CreditRest != 24*60 {
		t.Fatalf("credit rest was %d, want 1440", p.CreditRest)
	}
	if err := creditRestCmd([]string{"credit-rest-cli", "9d"}); err == nil {
		t.Fatal("a rest beyond eight days accepted")
	}
	if err := creditRestCmd([]string{"credit-rest-cli", "default"}); err != nil {
		t.Fatal(err)
	}
	p, err = provider.Find("credit-rest-cli")
	if err != nil || p.CreditRest != 0 {
		t.Fatalf("default left %d (%v)", p.CreditRest, err)
	}
}
