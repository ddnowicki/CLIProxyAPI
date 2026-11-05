package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestModelCooldownErrorClientReset(t *testing.T) {
	for _, duration := range []time.Duration{0, time.Millisecond, 3723*time.Second + time.Millisecond, 27 * time.Hour} {
		t.Run(duration.String(), func(t *testing.T) {
			err := newModelCooldownErrorWithCause("claude-opus-5-5", "claude", duration, errors.New("upstream (reset after 999999s)"))
			seconds := err.resetSeconds()
			message := ojson.Get(err.Error(), "error.message").String()
			want := fmt.Sprintf("(reset after %ds)", seconds)
			if !strings.Contains(message, want) || strings.Index(message, want) > strings.Index(message, "last error") {
				t.Fatalf("trusted reset must precede upstream diagnostic: %s", message)
			}
			if got := err.Headers().Get("Retry-After"); got != fmt.Sprint(seconds) {
				t.Fatalf("Retry-After=%s, want %d", got, seconds)
			}
			if got := ojson.Get(err.Error(), "error.reset_seconds").Int(); got != int64(seconds) {
				t.Fatalf("reset_seconds=%d, want %d", got, seconds)
			}
			if string(err.ResponseBody()) != err.Error() {
				t.Fatal("response body discarded pool error")
			}
		})
	}
	err := &modelCooldownError{resetIn: time.Hour, resetAt: time.Now().Add(-time.Second)}
	if err.resetSeconds() != 0 || err.Headers().Get("Retry-After") != "0" {
		t.Fatal("expired absolute deadline must not re-advertise original duration")
	}
}

func TestPoolCooldownCombinesGlobalAndModelBlocks(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		global, model time.Duration
		want          time.Duration
	}{
		{"model outlives global", time.Hour, 3 * time.Hour, 3 * time.Hour},
		{"global outlives model", 3 * time.Hour, time.Hour, 3 * time.Hour},
		{"expired global", -time.Hour, time.Hour, time.Hour},
		{"global with available model", time.Hour, -time.Hour, time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := poolCooldownTestAuth("combined", now.Add(tc.model))
			auth.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(tc.global)}
			blocked, reason, next := isAuthBlockedForModel(auth, "claude-opus-5-5", now)
			if !blocked || reason != blockReasonCooldown || !next.Equal(now.Add(tc.want)) {
				t.Fatalf("got (%v,%v,%v), want quota until %v", blocked, reason, next, now.Add(tc.want))
			}
		})
	}
	auth := poolCooldownTestAuth("disabled-model", now.Add(time.Hour))
	auth.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(2 * time.Hour)}
	auth.ModelStates["claude-opus-5-5"].Status = StatusDisabled
	if _, reason, next := isAuthBlockedForModel(auth, "claude-opus-5-5", now); reason != blockReasonDisabled || !next.IsZero() {
		t.Fatal("disabled model must not be advertised as recovering at quota reset")
	}
	auth.ModelStates["claude-opus-5-5"].Status = StatusActive
	auth.Quota.NextRecoverAt = time.Time{}
	if blocked, _, next := isAuthBlockedForModel(auth, "claude-opus-5-5", now); !blocked || !next.IsZero() {
		t.Fatal("unknown global reset must not be hidden by a known model reset")
	}
}

func TestSharedCooldownHelpersAcrossProviders(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, provider := range []string{"claude", "codex", "grok"} {
		t.Run(provider, func(t *testing.T) {
			auth := poolCooldownTestAuth("shared-"+provider, now.Add(2*time.Hour))
			auth.Provider = provider
			auth.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Hour)}
			blocked, reason, next := isAuthBlockedForModel(auth, "claude-opus-5-5", now)
			if !blocked || reason != blockReasonCooldown || !next.Equal(now.Add(2*time.Hour)) {
				t.Fatalf("shared block calculation differs for %s", provider)
			}
			err := newModelCooldownError("test-model", provider, time.Hour)
			if err.StatusCode() != 429 || ojson.Get(err.Error(), "error.code").String() != "model_cooldown" || err.Headers().Get("Retry-After") != "3600" {
				t.Fatalf("shared cooldown contract changed for %s", provider)
			}
		})
	}
}

func poolCooldownTestAuth(id string, deadline time.Time) *Auth {
	return &Auth{
		ID: id, Provider: "claude", Status: StatusActive,
		ModelStates: map[string]*ModelState{
			"claude-opus-5-5": {
				Status: StatusError, Unavailable: true, NextRetryAfter: deadline,
				Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline},
			},
		},
	}
}

func poolCooldownTestManager(t *testing.T, auths ...*Auth) *Manager {
	t.Helper()
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.RegisterExecutor(&poolCooldownExecutor{})
	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "claude-opus-5-5"}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		m.auths[auth.ID] = auth
	}
	return m
}

func TestPoolCooldownDeadlineEligibility(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		mutate func(*Manager, *Auth, *Auth)
		opts   cliproxyexecutor.Options
		want   time.Duration
		known  bool
	}{
		{name: "earliest account", want: time.Hour, known: true},
		{name: "disabled account ignored", mutate: func(_ *Manager, a, _ *Auth) { a.Disabled = true }, want: 3 * time.Hour, known: true},
		{name: "unsupported account ignored", mutate: func(_ *Manager, a, _ *Auth) {
			registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: "claude-haiku-5-5"}})
		}, want: 3 * time.Hour, known: true},
		{name: "available account prevents pool cooldown", mutate: func(_ *Manager, a, _ *Auth) { a.ModelStates = nil }},
		{name: "expired deadline is available", mutate: func(_ *Manager, a, _ *Auth) {
			a.ModelStates["claude-opus-5-5"].NextRetryAfter = now.Add(-time.Hour)
			a.ModelStates["claude-opus-5-5"].Quota.NextRecoverAt = now.Add(-time.Hour)
		}},
		{name: "unknown deadline is not available", mutate: func(_ *Manager, a, _ *Auth) {
			a.ModelStates["claude-opus-5-5"].NextRetryAfter = time.Time{}
			a.ModelStates["claude-opus-5-5"].Quota.NextRecoverAt = time.Time{}
		}, want: 3 * time.Hour, known: true},
		{name: "no registered executor", mutate: func(m *Manager, _, _ *Auth) { m.executors = nil }},
		{name: "weighted zero account ignored", mutate: func(m *Manager, a, _ *Auth) {
			m.selector = &WeightedRoundRobinSelector{}
			a.Attributes = map[string]string{"weight": "0"}
		}, want: 3 * time.Hour, known: true},
		{name: "weighted affinity zero account ignored", mutate: func(m *Manager, a, _ *Auth) {
			m.selector = NewSessionAffinitySelector(&WeightedRoundRobinSelector{})
			a.Attributes = map[string]string{"weight": "0"}
		}, want: 3 * time.Hour, known: true},
		{name: "pinned account", opts: cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "pool-pinned account-b"}}, want: 3 * time.Hour, known: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := poolCooldownTestAuth("pool-"+tc.name+"-a", now.Add(time.Hour))
			b := poolCooldownTestAuth("pool-"+tc.name+"-b", now.Add(3*time.Hour))
			m := poolCooldownTestManager(t, a, b)
			if tc.mutate != nil {
				tc.mutate(m, a, b)
			}
			deadline, known := m.poolCooldownDeadline(context.Background(), []string{"claude"}, "claude-opus-5-5", tc.opts, now)
			if known != tc.known || (known && !deadline.Equal(now.Add(tc.want))) {
				t.Fatalf("deadline=%v known=%v, want offset=%v known=%v", deadline, known, tc.want, tc.known)
			}
		})
	}
}

func TestPoolCooldownResolvesOAuthAlias(t *testing.T) {
	now := time.Now()
	a := poolCooldownTestAuth("pool-alias", now.Add(time.Hour))
	m := poolCooldownTestManager(t, a)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"claude": {{Name: "claude-opus-5-5", Alias: "my-opus"}}})
	deadline, known := m.poolCooldownDeadline(context.Background(), []string{"claude"}, "my-opus", cliproxyexecutor.Options{}, now)
	if !known || !deadline.Equal(now.Add(time.Hour)) {
		t.Fatalf("alias deadline=%v known=%v", deadline, known)
	}
}

func TestTerminalPoolCooldownHeaderOverridesLastAttempt(t *testing.T) {
	now := time.Now()
	m := poolCooldownTestManager(t, poolCooldownTestAuth("pool-header", now.Add(time.Minute)))
	lastHint := 24 * time.Hour
	last := withTrustedRetryAfter(&Error{HTTPStatus: 429, Message: "upstream last account"}, &lastHint)
	err := m.terminalPoolCooldownError(context.Background(), []string{"claude"}, "claude-opus-5-5", cliproxyexecutor.Options{}, last)
	var pooled *modelCooldownError
	if !errors.As(err, &pooled) || pooled.resetAt.IsZero() || !errors.Is(err, last) {
		t.Fatalf("expected pool summary retaining last error: %v", err)
	}
	if got := SafeResponseHeaders(err).Get("Retry-After"); got != "60" {
		t.Fatalf("last attempt replaced pool deadline: Retry-After=%s", got)
	}
	for _, original := range []error{&Error{HTTPStatus: 503}, &requestScopedStatusError{status: 429}} {
		if got := m.terminalPoolCooldownError(context.Background(), []string{"claude"}, "claude-opus-5-5", cliproxyexecutor.Options{}, original); got != original {
			t.Fatalf("must preserve non-quota/request-scoped error: %v", got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := m.terminalPoolCooldownError(ctx, []string{"claude"}, "claude-opus-5-5", cliproxyexecutor.Options{}, last); got != last {
		t.Fatal("must preserve cancellation")
	}
}

type poolCooldownExecutor struct {
	hints     map[string]time.Duration
	bootstrap bool
}

func (*poolCooldownExecutor) Identifier() string                                { return "claude" }
func (*poolCooldownExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }
func (*poolCooldownExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
func (e *poolCooldownExecutor) Execute(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	hint := e.hints[a.ID]
	return cliproxyexecutor.Response{}, issue7017HintError{msg: "synthetic capacity rejection", hint: &hint}
}
func (e *poolCooldownExecutor) CountTokens(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, a, r, o)
}
func (e *poolCooldownExecutor) ExecuteStream(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_, err := e.Execute(ctx, a, r, o)
	if !e.bootstrap {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Err: err}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestPoolCooldownAliasPoolDoesNotRotate(t *testing.T) {
	now := time.Now()
	m := newOpenAICompatPoolTestManager(t, "public", []internalconfig.OpenAICompatibilityModel{
		{Name: "upstream-a", Alias: "public"},
		{Name: "upstream-b", Alias: "public"},
	}, nil)
	for _, a := range m.auths {
		a.ModelStates = map[string]*ModelState{
			"upstream-a": {Unavailable: true, NextRetryAfter: now.Add(time.Minute), Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Minute)}},
			"upstream-b": {Unavailable: true, NextRetryAfter: now.Add(time.Hour), Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}},
		}
	}
	deadline, known := m.poolCooldownDeadline(context.Background(), []string{openAICompatPoolProviderKey}, "public", cliproxyexecutor.Options{}, now)
	if !known || !deadline.Equal(now.Add(time.Minute)) {
		t.Fatalf("pooled alias deadline=%v known=%v", deadline, known)
	}
	if len(m.modelPoolOffsets) != 0 {
		t.Fatal("read-only availability calculation rotated execution pool")
	}
}

type poolCooldownHeaderError struct{ headers http.Header }

func (poolCooldownHeaderError) Error() string { return "synthetic upstream rejection" }
func (e poolCooldownHeaderError) Headers() http.Header {
	if e.headers != nil {
		return e.headers
	}
	return http.Header{
		"x-request-id":                      []string{"synthetic-request"},
		"Request-Id":                        []string{"synthetic-provider-request"},
		"Retry-After":                       []string{"86400"},
		"Set-Cookie":                        []string{"synthetic-cookie"},
		"set-cookie":                        []string{"synthetic-lowercase-cookie"},
		"Authorization":                     []string{"synthetic-auth"},
		"Proxy-Authorization":               []string{"synthetic-proxy-auth"},
		"Connection":                        []string{"X-Internal-Secret"},
		"X-Internal-Secret":                 []string{"synthetic-secret"},
		"Access-Control-Allow-Origin":       []string{"*"},
		"Content-Length":                    []string{"999999"},
		"Content-Encoding":                  []string{"gzip"},
		"Anthropic-Ratelimit-Unified-Reset": []string{"9999999999"},
	}
}

func TestPoolCooldownPreservesUpstreamHeaders(t *testing.T) {
	err := &modelCooldownError{resetIn: time.Minute, resetAt: time.Now().Add(time.Minute), cause: poolCooldownHeaderError{}}
	if got := err.Headers().Get("X-Request-Id"); got != "synthetic-request" {
		t.Fatalf("upstream request ID lost: %q", got)
	}
	if got := err.Headers().Get("Retry-After"); got != "60" {
		t.Fatalf("upstream deadline overrode pool: %q", got)
	}
	if got := err.Headers().Get("Request-Id"); got != "synthetic-provider-request" {
		t.Fatalf("upstream request ID lost: %q", got)
	}
	for key := range err.Headers() {
		switch key {
		case "Request-Id", "X-Request-Id", "Content-Type", "Retry-After":
		default:
			t.Fatalf("non-allowlisted upstream header forwarded: %s", key)
		}
	}
	if got := SafeResponseHeaders(err).Get("X-Request-Id"); got != "" {
		t.Fatalf("unsafe upstream header forwarded without passthrough: %q", got)
	}
}

func TestPoolCooldownDropsConnectionScopedRequestIDs(t *testing.T) {
	upstream := http.Header{
		"connection":   []string{" request-id, X-Request-ID "},
		"Request-Id":   []string{"synthetic-request"},
		"x-request-id": []string{"synthetic-other-request"},
	}
	err := &modelCooldownError{resetIn: time.Minute, cause: poolCooldownHeaderError{headers: upstream}}
	for _, key := range []string{"Request-Id", "X-Request-Id", "Connection"} {
		if got := err.Headers().Get(key); got != "" {
			t.Fatalf("connection-scoped header %s forwarded: %q", key, got)
		}
	}
	if upstream.Get("Request-Id") != "synthetic-request" {
		t.Fatal("upstream header map mutated")
	}
}

func TestTerminalPoolCooldownLeavesOtherProvidersUnchanged(t *testing.T) {
	m := poolCooldownTestManager(t, poolCooldownTestAuth("provider-scope", time.Now().Add(time.Hour)))
	original := &Error{HTTPStatus: 429, Message: "synthetic provider error"}
	for _, providers := range [][]string{{"codex"}, {"claude", "codex"}, {"plugin-provider"}} {
		if got := m.terminalPoolCooldownError(context.Background(), providers, "claude-opus-5-5", cliproxyexecutor.Options{}, original); got != original {
			t.Fatalf("changed non-Claude execution for %v", providers)
		}
	}
}

func TestTerminalPoolCooldownAcrossExecutionModes(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream", "stream-bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			m := NewManager(nil, &FillFirstSelector{}, nil)
			m.SetRetryConfig(0, 0, 0)
			executor := &poolCooldownExecutor{hints: map[string]time.Duration{"pool-" + mode + "-a": time.Minute, "pool-" + mode + "-z": time.Hour}, bootstrap: mode == "stream-bootstrap"}
			m.RegisterExecutor(executor)
			for id := range executor.hints {
				registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: "claude-opus-5-5"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, err := m.Register(WithSkipPersist(context.Background()), &Auth{ID: id, Provider: "claude"}); err != nil {
					t.Fatal(err)
				}
			}
			req := cliproxyexecutor.Request{Model: "claude-opus-5-5"}
			var err error
			switch mode {
			case "execute":
				_, err = m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
			case "count":
				_, err = m.ExecuteCount(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
			default:
				var result *cliproxyexecutor.StreamResult
				result, err = m.ExecuteStream(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{Stream: true})
				if err == nil && result != nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
					}
				}
			}
			if err == nil {
				t.Fatal("expected terminal rejection")
			}
			if got := SafeResponseHeaders(err).Get("Retry-After"); got != "60" {
				t.Fatalf("Retry-After=%q, want earliest account 60, err=%v", got, err)
			}
			if !strings.Contains(err.Error(), "(reset after 60s)") {
				t.Fatalf("missing client-readable reset: %v", err)
			}
			var body interface{ ResponseBody() []byte }
			if !errors.As(err, &body) || !strings.Contains(string(body.ResponseBody()), "(reset after 60s)") {
				t.Fatal("Claude error normalization must see the pool error body")
			}
		})
	}
}
