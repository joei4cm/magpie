package provider

import (
	"testing"
	"time"
)

func TestSetCreditRest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "credit-test", Name: "Credit", Chat: "https://example.invalid/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := SetCreditRest("credit-test", 24*60); err != nil {
		t.Fatal(err)
	}
	p, err := Find("credit-test")
	if err != nil {
		t.Fatal(err)
	}
	if p.CreditRest != 24*60 || p.CreditRestFor() != 24*time.Hour {
		t.Fatalf("credit rest was not saved: %d / %v", p.CreditRest, p.CreditRestFor())
	}
	if err := SetCreditRest("credit-test", MaxQuotaRest+1); err == nil {
		t.Fatal("credit rest beyond eight days accepted")
	}
	p, err = Find("credit-test")
	if err != nil || p.CreditRest != 24*60 {
		t.Fatalf("invalid value changed saved setting: %+v, %v", p, err)
	}
	if err := SetCreditRest("credit-test", 0); err != nil {
		t.Fatal(err)
	}
	p, err = Find("credit-test")
	if err != nil || p.CreditRest != 0 || p.CreditRestFor() != 0 {
		t.Fatalf("default did not restore the ladder: %+v, %v", p, err)
	}
}
