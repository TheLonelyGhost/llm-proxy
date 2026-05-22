package ibmbob_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/providers/ibmbob"
)

func TestBudget_LegacyKey_Unsupported(t *testing.T) {
	block := buildBlock(t, `api_key = "sk-test"`)
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Budget(context.Background()); !errors.Is(err, backend.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestBudget_AuthnBackend_HappyPath(t *testing.T) {
	fb := &fakeBackend{t: t, expectSign: true}
	fb.hmacSecret = []byte("topsecret")
	// Two instances + two teams to exercise the selection logic.
	fb.profileResp = `{
        "user_id": "user-42",
        "instances": [
          {"instance_id":"inst-1","instance_name":"prod","teams":[
             {"id":"team-1","name":"Platform","budget_limit":100,"usage":12.5},
             {"id":"team-2","name":"Other","budget_limit":50,"usage":1}
          ]},
          {"instance_id":"inst-2","instance_name":"dev","teams":[
             {"id":"team-x","name":"Dev","budget_limit":10,"usage":0}
          ]}
        ]
    }`
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key      = "%s"
        hmac_secret  = "topsecret"
        api_base     = "%s"
        instance_id  = "inst-1"
        team_id      = "team-1"
    `, makeJWTKey(), srv.URL))

	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}

	bud, err := p.Budget(context.Background())
	if err != nil {
		t.Fatalf("Budget: %v", err)
	}
	if bud.Currency != "bobcoins" {
		t.Errorf("Currency = %q, want %q", bud.Currency, "bobcoins")
	}
	if bud.MaxBudget != 100 {
		t.Errorf("MaxBudget = %v, want 100", bud.MaxBudget)
	}
	if bud.Spend != 12.5 {
		t.Errorf("Spend = %v, want 12.5", bud.Spend)
	}
	if bud.Remaining != 87.5 {
		t.Errorf("Remaining = %v, want 87.5", bud.Remaining)
	}
	if bud.Unlimited {
		t.Error("Unlimited should be false")
	}
	if bud.Extras["instance_id"] != "inst-1" || bud.Extras["team_id"] != "team-1" {
		t.Errorf("Extras = %+v", bud.Extras)
	}
	if bud.Extras["instance_name"] != "prod" || bud.Extras["team_name"] != "Platform" {
		t.Errorf("Extras names = %+v", bud.Extras)
	}
	if bud.Extras["user_id"] != "user-42" {
		t.Errorf("Extras user_id = %q", bud.Extras["user_id"])
	}
}

func TestBudget_DefaultsToFirstInstanceAndTeam(t *testing.T) {
	fb := &fakeBackend{t: t, expectSign: true}
	fb.hmacSecret = []byte("topsecret")
	fb.profileResp = `{"user_id":"u","instances":[
       {"instance_id":"inst-1","teams":[
          {"id":"team-1","budget_limit":7,"usage":3}
       ]},
       {"instance_id":"inst-2","teams":[{"id":"team-x","budget_limit":99,"usage":0}]}
    ]}`
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key     = "%s"
        hmac_secret = "topsecret"
        api_base    = "%s"
    `, makeJWTKey(), srv.URL))

	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	bud, err := p.Budget(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bud.MaxBudget != 7 || bud.Spend != 3 || bud.Remaining != 4 {
		t.Errorf("budget = %+v", bud)
	}
}

func TestBudget_UnlimitedBudgetSentinel(t *testing.T) {
	fb := &fakeBackend{t: t, expectSign: true}
	fb.hmacSecret = []byte("topsecret")
	fb.profileResp = `{"instances":[{"instance_id":"i","teams":[
       {"id":"t","budget_limit":-1,"usage":5}
    ]}]}`
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key     = "%s"
        hmac_secret = "topsecret"
        api_base    = "%s"
    `, makeJWTKey(), srv.URL))

	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	bud, err := p.Budget(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bud.Unlimited {
		t.Error("expected Unlimited=true for budget_limit=-1")
	}
	if bud.MaxBudget != 0 || bud.Remaining != 0 {
		t.Errorf("max/remaining should be zero for unlimited; got %+v", bud)
	}
	if bud.Spend != 5 {
		t.Errorf("Spend = %v, want 5", bud.Spend)
	}
}

func TestBudget_EmptyProfileIsUnsupported(t *testing.T) {
	fb := &fakeBackend{t: t, expectSign: true}
	fb.hmacSecret = []byte("topsecret")
	fb.profileResp = `{"instances":[]}`
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key     = "%s"
        hmac_secret = "topsecret"
        api_base    = "%s"
    `, makeJWTKey(), srv.URL))

	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Budget(context.Background())
	if !errors.Is(err, backend.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestBudget_UpstreamErrorPropagates(t *testing.T) {
	fb := &fakeBackend{t: t, expectSign: true}
	fb.hmacSecret = []byte("topsecret")
	// profileResp left empty -> handler returns 500.
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key     = "%s"
        hmac_secret = "topsecret"
        api_base    = "%s"
    `, makeJWTKey(), srv.URL))

	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Budget(context.Background())
	if err == nil || !strings.Contains(err.Error(), "profile fetch failed") {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, backend.ErrUnsupported) {
		t.Errorf("upstream HTTP 500 should not be classified as ErrUnsupported")
	}
}
