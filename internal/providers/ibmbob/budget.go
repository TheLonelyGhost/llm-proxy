package ibmbob

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"go.opentelemetry.io/otel/attribute"
)

// budgetCurrency is the unit IBM Bob reports balances in. The official CLI
// surfaces the same value to humans as "Bobcoins".
const budgetCurrency = "bobcoins"

// budgetRawTeam carries every field of /admin/v1/profile teams[] that the
// proxy reads. It is intentionally a superset of the fields used for
// identity selection so the budget code can reuse the same upstream call.
type budgetRawTeam struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	BudgetLimit float64 `json:"budget_limit"`
	Usage       float64 `json:"usage"`
}

type budgetRawInstance struct {
	InstanceID   string          `json:"instance_id"`
	InstanceName string          `json:"instance_name"`
	Teams        []budgetRawTeam `json:"teams"`
}

type budgetRawProfile struct {
	UserID    string              `json:"user_id"`
	Instances []budgetRawInstance `json:"instances"`
}

// Budget implements backend.BudgetReporter for ibmbob.
//
// It performs a fresh GET /admin/v1/profile (no caching, matching the
// proxy's "always fetch" policy for this surface) and picks the team
// according to the same preference rules used elsewhere: an operator-pinned
// instance_id / team_id wins; otherwise the first instance and team in the
// response are used.
//
// The legacy (sk-/pk-) key path talks to a backend that does not expose
// /admin/v1/profile, so we return backend.ErrUnsupported in that case
// rather than fabricating a value.
func (p *Provider) Budget(ctx context.Context) (backend.Budget, error) {
	ctx, span := p.tracer.Start(ctx, "ibmbob.budget")
	defer span.End()

	if p.keyKind == KeyKindLegacy {
		return backend.Budget{}, fmt.Errorf("%w: ibm-bob legacy API keys do not expose budget info",
			backend.ErrUnsupported)
	}

	body, err := p.fetchProfileBody(ctx)
	if err != nil {
		span.RecordError(err)
		return backend.Budget{}, err
	}

	bud, err := pickBudget(body, p.settings.InstanceID, p.settings.TeamID)
	if err != nil {
		span.RecordError(err)
		return backend.Budget{}, err
	}
	span.SetAttributes(
		attribute.Float64("budget.max", bud.MaxBudget),
		attribute.Float64("budget.spend", bud.Spend),
		attribute.Bool("budget.unlimited", bud.Unlimited),
	)
	return bud, nil
}

// fetchProfileBody issues the GET /admin/v1/profile call with the full
// header/signing dance and returns the raw JSON body. It is a sibling of
// fetchIdentity that returns bytes instead of an Identity so the budget and
// identity paths can share the same upstream contract.
func (p *Provider) fetchProfileBody(ctx context.Context) ([]byte, error) {
	target, pathAndQuery, err := p.buildURL(profilePath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build profile request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.settings.UserAgent)
	budgetCred, budgetKind := p.currentCredential()
	req.Header.Set("Authorization", AuthorizationHeader(budgetCred, budgetKind))
	id := p.currentIdentity()
	if id.InstanceID != "" {
		req.Header.Set("x-instance-id", id.InstanceID)
	}
	if id.TeamID != "" {
		req.Header.Set("x-team-id", id.TeamID)
	}
	if len(p.hmacSecret) > 0 && shouldSignURL(target, p.baseURL) {
		sig := SignRequest(http.MethodGet, pathAndQuery, nil, p.hmacSecret, p.now())
		req.Header.Set("x-request-timestamp", sig.Timestamp)
		req.Header.Set("x-request-signature", sig.Signature)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errProfileFetch, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("%w: nil response", errProfileFetch)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read profile body: %w", err)
	}
	if resp.StatusCode >= 400 {
		// Surface upstream error with a small body snippet for debugging,
		// without leaking secrets (the body is the JSON IBM returns).
		return nil, fmt.Errorf("%w: HTTP %d: %s", errProfileFetch, resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// pickBudget mirrors the reference behaviour in /opt/homebrew/bin/bob:
//   - find the requested instance (by instance_id) or fall back to the first;
//   - within that instance, find the requested team (by id) or first;
//   - normalise budget_limit=-1 ("unlimited") to MaxBudget=0 with
//     Unlimited=true, matching the upstream sentinel.
//
// Returns ErrUnsupported when the profile contains no instances or no teams,
// because in that case the upstream has nothing to report.
func pickBudget(body []byte, requestedInstance, requestedTeam string) (backend.Budget, error) {
	var profile budgetRawProfile
	if err := json.Unmarshal(body, &profile); err != nil {
		return backend.Budget{}, fmt.Errorf("%w: decode: %w", errProfileFetch, err)
	}
	if len(profile.Instances) == 0 {
		return backend.Budget{}, fmt.Errorf("%w: no instances on profile", backend.ErrUnsupported)
	}
	chosen := profile.Instances[0]
	if requestedInstance != "" {
		for _, inst := range profile.Instances {
			if inst.InstanceID == requestedInstance {
				chosen = inst
				break
			}
		}
	}
	if len(chosen.Teams) == 0 {
		return backend.Budget{}, fmt.Errorf("%w: instance %q has no teams", backend.ErrUnsupported, chosen.InstanceID)
	}
	team := chosen.Teams[0]
	if requestedTeam != "" {
		for _, t := range chosen.Teams {
			if t.ID == requestedTeam {
				team = t
				break
			}
		}
	}

	bud := backend.Budget{
		Currency:  budgetCurrency,
		MaxBudget: team.BudgetLimit,
		Spend:     team.Usage,
		Extras:    map[string]string{},
	}
	if team.BudgetLimit < 0 {
		// Upstream sentinel: -1 means "no ceiling". Surface that as
		// Unlimited=true and zero out MaxBudget so consumers don't display
		// a negative number.
		bud.Unlimited = true
		bud.MaxBudget = 0
	}
	if bud.MaxBudget > 0 {
		bud.Remaining = bud.MaxBudget - bud.Spend
	}
	if profile.UserID != "" {
		bud.Extras["user_id"] = profile.UserID
	}
	if chosen.InstanceID != "" {
		bud.Extras["instance_id"] = chosen.InstanceID
	}
	if chosen.InstanceName != "" {
		bud.Extras["instance_name"] = chosen.InstanceName
	}
	if team.ID != "" {
		bud.Extras["team_id"] = team.ID
	}
	if team.Name != "" {
		bud.Extras["team_name"] = team.Name
	}
	if len(bud.Extras) == 0 {
		bud.Extras = nil
	}
	return bud, nil
}

// Static interface assertions: ensure the provider keeps satisfying both
// the core Backend and the optional BudgetReporter capability.
var (
	_ backend.Backend        = (*Provider)(nil)
	_ backend.BudgetReporter = (*Provider)(nil)
)
