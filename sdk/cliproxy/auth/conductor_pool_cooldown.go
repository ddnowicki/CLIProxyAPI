package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// terminalPoolCooldownError reports availability for a new request, independently
// of the exhausted credential/retry budgets of the request that just finished.
func (m *Manager) terminalPoolCooldownError(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, err error) error {
	if err == nil || m == nil || m.HomeEnabled() || m.hasPluginScheduler() {
		return err
	}
	if len(providers) != 1 || canonicalSchedulingProvider(providers[0]) != "claude" {
		return err
	}
	if statusCodeFromError(err) != http.StatusTooManyRequests || isRequestScopedError(err) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return err
	}
	now := time.Now()
	routeModel := authSelectionModelFromOptions(opts, model)
	deadline, ok := m.poolCooldownDeadline(ctx, providers, routeModel, opts, now)
	if !ok {
		return err
	}
	return &modelCooldownError{
		model:   model,
		resetIn: deadline.Sub(now),
		resetAt: deadline,
		cause:   err,
	}
}

func (m *Manager) poolCooldownDeadline(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, now time.Time) (time.Time, bool) {
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerSet[canonicalSchedulingProvider(strings.TrimSpace(provider))] = struct{}{}
	}
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	registryRef := registry.GetGlobalRegistry()

	m.mu.RLock()
	defer m.mu.RUnlock()
	selector := m.selector
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		selector = affinity.fallback
	}
	if selector != nil && !isBuiltInSelector(selector) {
		return time.Time{}, false
	}
	requirePositiveWeight := selectorStrategy(selector) == schedulerStrategyWeightedRoundRobin
	var earliest time.Time
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled || candidate.Status == StatusDisabled {
			continue
		}
		provider := canonicalSchedulingProvider(executorKeyFromAuth(candidate))
		if _, included := providerSet[provider]; !included {
			continue
		}
		if _, registered := m.executorLocked(provider); !registered {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if requirePositiveWeight && authWeight(candidate) <= 0 {
			continue
		}
		if !eligibility.allows(candidate) || (model != "" && !m.authSupportsRouteModel(registryRef, candidate, model)) {
			continue
		}

		models, pooled, _, _ := m.executionModelCandidatesWithAliasRotation(candidate, model, false)
		for _, upstreamModel := range models {
			stateModel := m.stateModelForExecution(candidate, model, upstreamModel, pooled)
			blocked, _, next := isAuthBlockedForModel(candidate, stateModel, now)
			if !blocked {
				return time.Time{}, false
			}
			if next.After(now) && (earliest.IsZero() || next.Before(earliest)) {
				earliest = next
			}
		}
	}
	return earliest, !earliest.IsZero()
}
