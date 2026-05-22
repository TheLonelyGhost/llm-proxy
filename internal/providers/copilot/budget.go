package copilot

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"go.opentelemetry.io/otel/attribute"
)

// Currency tags reported under backend.Budget.Currency. These are the
// short, lowercase, snake_case identifiers documented in the README.
const (
	currencyPremiumRequests = "premium_requests"
	currencyInteractions    = "interactions"
)

// Snapshot key names used by GitHub's /copilot_internal/v2/token response.
// Keeping them as constants makes the budget-mapping precedence explicit.
const (
	snapshotPremiumInteractions = "premium_interactions"
	snapshotChat                = "chat"
	snapshotCompletions         = "completions"
)

// Budget implements backend.BudgetReporter for the GitHub Copilot backend.
//
// Source: GitHub's native `GET /copilot_internal/user` endpoint - the same
// surface the official editor extensions read to populate the
// "Manage Copilot" panel. It works across plan tiers (Individual / Pro /
// Pro+ / Business / Enterprise), uses the stored GitHub OAuth token
// directly, and has no side effects on the cached short-lived API token.
//
// Mapping precedence for the primary fields:
//
//  1. quota_snapshots.premium_interactions (modern paid plans). Currency:
//     "premium_requests".
//  2. quota_snapshots.chat (older / Free plans that surface chat as the
//     primary counter). Currency: "interactions".
//  3. limited_user_quotas (legacy free tier; only remaining counts, no
//     entitlement). Currency: "interactions", max_budget left at 0.
//
// Per-category numbers, reset dates, overage flags, and identifying
// metadata (copilot_plan, organization_login) are exposed under Extras
// with deterministic keys so downstream tooling can present them stably.
//
// Returns backend.ErrUnsupported when GitHub returns no quota information
// at all (very old free accounts, or accounts where Copilot is not
// entitled).
func (p *Provider) Budget(ctx context.Context) (backend.Budget, error) {
	ctx, span := p.tracer.Start(ctx, "copilot.budget")
	defer span.End()

	info, err := p.auth.FetchUserInfo(ctx)
	if err != nil {
		span.RecordError(err)
		return backend.Budget{}, err
	}

	bud, ok := mapCopilotBudget(info)
	if !ok {
		return backend.Budget{}, fmt.Errorf("%w: copilot account has no quota snapshot", backend.ErrUnsupported)
	}
	span.SetAttributes(
		attribute.String("budget.currency", bud.Currency),
		attribute.Float64("budget.max", bud.MaxBudget),
		attribute.Float64("budget.spend", bud.Spend),
		attribute.Bool("budget.unlimited", bud.Unlimited),
	)
	return bud, nil
}

// mapCopilotBudget translates a /copilot_internal/user response to
// backend.Budget. Returned ok=false means the response contains no usable
// quota signal at all. Pure for testability.
func mapCopilotBudget(info *CopilotUserInfo) (backend.Budget, bool) {
	if info == nil {
		return backend.Budget{}, false
	}

	bud := backend.Budget{Extras: map[string]string{}}

	switch {
	case hasSnapshot(info.QuotaSnapshots, snapshotPremiumInteractions):
		applySnapshot(&bud, info.QuotaSnapshots[snapshotPremiumInteractions], currencyPremiumRequests, snapshotPremiumInteractions)
	case hasSnapshot(info.QuotaSnapshots, snapshotChat):
		applySnapshot(&bud, info.QuotaSnapshots[snapshotChat], currencyInteractions, snapshotChat)
	case len(info.LimitedUserQuotas) > 0:
		// Free-tier path: no entitlement available; expose counters as
		// remaining and leave max_budget=0 with unlimited=false so a
		// consumer knows the ceiling is unknown.
		bud.Currency = currencyInteractions
		// Choose chat as the primary remaining number when present;
		// otherwise pick deterministically (sorted) so the response
		// is stable.
		key := pickLimitedUserPrimary(info.LimitedUserQuotas)
		bud.Spend = 0
		bud.Remaining = info.LimitedUserQuotas[key]
		bud.Extras["limited_user_primary"] = key
	default:
		return backend.Budget{}, false
	}

	// Always expose every snapshot the upstream returned, in addition to
	// the chosen primary, so dashboards can show "chat: ..., completions:
	// ..., premium: ...".
	for _, key := range sortedKeys(info.QuotaSnapshots) {
		s := info.QuotaSnapshots[key]
		prefix := "snapshot_" + key
		bud.Extras[prefix+"_entitlement"] = formatFloat(s.Entitlement)
		bud.Extras[prefix+"_remaining"] = formatFloat(s.Remaining)
		if s.PercentRemaining != 0 {
			bud.Extras[prefix+"_percent_remaining"] = formatFloat(s.PercentRemaining)
		}
		if s.Unlimited {
			bud.Extras[prefix+"_unlimited"] = "true"
		}
		if s.OverageCount != 0 {
			bud.Extras[prefix+"_overage_count"] = formatFloat(s.OverageCount)
		}
		if s.OveragePermitted {
			bud.Extras[prefix+"_overage_permitted"] = "true"
		}
	}
	for _, key := range sortedKeys(info.LimitedUserQuotas) {
		bud.Extras["limited_user_"+key] = formatFloat(info.LimitedUserQuotas[key])
	}
	if info.QuotaResetDate != "" {
		bud.Extras["quota_reset_date"] = info.QuotaResetDate
	}
	if info.QuotaResetDateUTC != "" {
		bud.Extras["quota_reset_date_utc"] = info.QuotaResetDateUTC
	}
	if info.LimitedUserResetDate != "" {
		bud.Extras["limited_user_reset_date"] = info.LimitedUserResetDate
	}
	if info.ChatEnabled {
		bud.Extras["chat_enabled"] = "true"
	}
	if info.CopilotPlan != "" {
		bud.Extras["copilot_plan"] = info.CopilotPlan
	}
	if info.AccessTypeSKU != "" {
		bud.Extras["access_type_sku"] = info.AccessTypeSKU
	}
	if info.Login != "" {
		bud.Extras["login"] = info.Login
	}
	if len(info.OrganizationList) > 0 {
		// First org is sufficient for display purposes; the rest is
		// available via the raw endpoint if a consumer needs it.
		bud.Extras["organization_login"] = info.OrganizationList[0].Login
		if info.OrganizationList[0].Name != "" {
			bud.Extras["organization_name"] = info.OrganizationList[0].Name
		}
	}

	if len(bud.Extras) == 0 {
		bud.Extras = nil
	}
	return bud, true
}

func hasSnapshot(m map[string]CopilotQuotaSnapshot, key string) bool {
	if m == nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// applySnapshot writes the primary fields onto bud from a single snapshot.
//
// Semantics intentionally match the ibmbob mapping so the two backends
// behave identically from a consumer's perspective:
//
//   - Unlimited => MaxBudget=0, Remaining=0, Unlimited=true
//   - Otherwise => MaxBudget=entitlement, Remaining=remaining,
//     Spend=entitlement-remaining (clamped at zero so overage usage doesn't
//     produce a negative Spend; the original overage_count is preserved in
//     Extras).
func applySnapshot(bud *backend.Budget, s CopilotQuotaSnapshot, currency, source string) {
	bud.Currency = currency
	bud.Extras["primary_source"] = source
	if s.Unlimited {
		bud.Unlimited = true
		bud.MaxBudget = 0
		bud.Remaining = 0
		// Spend is meaningless under unlimited; leave at zero. Overage
		// flags remain in Extras for visibility.
		return
	}
	bud.MaxBudget = s.Entitlement
	bud.Remaining = s.Remaining
	bud.Spend = s.Entitlement - s.Remaining
	if bud.Spend < 0 {
		bud.Spend = 0
	}
}

// pickLimitedUserPrimary chooses a deterministic primary key from the
// free-tier counter map. "chat" is preferred when present (it is what the
// official UI surfaces); otherwise the lowest-sorted key wins so the
// response is stable across calls. Callers must only invoke this when the
// map is non-empty; mapCopilotBudget guards on len(...) > 0 first.
func pickLimitedUserPrimary(m map[string]float64) string {
	if _, ok := m[snapshotChat]; ok {
		return snapshotChat
	}
	keys := sortedKeys(m)
	if len(keys) == 0 {
		// Defensive: caller invariant violated. Returning an empty key
		// is safer than panicking; the mapping will surface zeros.
		return ""
	}
	return keys[0]
}

// sortedKeys returns the keys of any map[string]V in ascending order.
// Implemented as a generic so it works for both snapshot maps and the
// raw float64 map. Returns nil for a nil or empty map so callers can
// range over the result safely.
func sortedKeys[V any](m map[string]V) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// formatFloat renders a float64 for use in Extras with the minimum number
// of digits that still round-trips. Whole numbers come back as plain
// integers ("300"); fractional values keep their precision ("95.83").
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Compile-time assertion: Provider satisfies the optional BudgetReporter
// capability.
var _ backend.BudgetReporter = (*Provider)(nil)
