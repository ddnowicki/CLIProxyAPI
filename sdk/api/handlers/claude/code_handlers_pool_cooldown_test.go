package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type poolHeaderRefusal struct{}

func (poolHeaderRefusal) Error() string              { return "synthetic capacity rejection" }
func (poolHeaderRefusal) StatusCode() int            { return http.StatusTooManyRequests }
func (poolHeaderRefusal) IsTransientRateLimit() bool { return true }
func (poolHeaderRefusal) RetryAfter() *time.Duration {
	value := time.Minute
	return &value
}
func (poolHeaderRefusal) Headers() http.Header {
	return http.Header{
		"X-Request-Id":                      []string{"synthetic-request"},
		"Set-Cookie":                        []string{"synthetic-cookie"},
		"Authorization":                     []string{"synthetic-auth"},
		"Connection":                        []string{"X-Private"},
		"X-Private":                         []string{"synthetic-private"},
		"Access-Control-Allow-Origin":       []string{"*"},
		"Anthropic-Ratelimit-Unified-Reset": []string{"9999999999"},
	}
}

type poolHeaderExecutor struct{ preFTKeepAliveClaudeExecutor }

func (*poolHeaderExecutor) Identifier() string { return "claude" }
func (*poolHeaderExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, poolHeaderRefusal{}
}

func TestClaudePoolCooldownFiltersCauseHeadersWithPassthrough(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(&poolHeaderExecutor{})
	const id = "pool-header-security"
	const model = "claude-opus-5-5"
	registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{ID: id, Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Execute(context.Background(), []string{"claude"}, coreexecutor.Request{Model: model}, coreexecutor.Options{})
	if err == nil {
		t.Fatal("expected synthetic quota error")
	}
	for _, passthrough := range []bool{false, true} {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: passthrough}, nil))
		handler.WriteErrorResponse(ctx, handlers.ExecutionErrorMessage(err))
		if recorder.Code != 429 || recorder.Header().Get("Retry-After") != "60" {
			t.Fatalf("unexpected status/deadline: %d %v", recorder.Code, recorder.Header())
		}
		for _, key := range []string{"Set-Cookie", "Authorization", "Connection", "X-Private", "Access-Control-Allow-Origin", "Anthropic-Ratelimit-Unified-Reset"} {
			if got := recorder.Header().Get(key); got != "" {
				t.Fatalf("passthrough=%v leaked %s=%q", passthrough, key, got)
			}
		}
		if got := recorder.Header().Get("X-Request-Id"); (got != "") != passthrough {
			t.Fatalf("request ID passthrough=%v header=%q", passthrough, got)
		}
		if !strings.Contains(recorder.Body.String(), "(reset after 60s)") {
			t.Fatal("reset message lost")
		}
	}
}

func TestClaudePoolCooldownPreservesResetMessageAndHeader(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "no passthrough"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			cfg := &sdkconfig.SDKConfig{PassthroughHeaders: passthrough}
			handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(cfg, nil))
			handler.WriteErrorResponse(ctx, &interfaces.ErrorMessage{
				StatusCode: http.StatusTooManyRequests,
				Error:      coreauth.NewModelCooldownError("claude-opus-5-5", "claude", 3723*time.Second),
				Addon:      http.Header{"Retry-After": []string{"86400"}},
			})
			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("status=%d", recorder.Code)
			}
			if got := recorder.Header().Get("Retry-After"); got != "3723" {
				t.Fatalf("Retry-After=%s", got)
			}
			body := recorder.Body.String()
			if got := ojson.Get(body, "error.type").String(); got != "rate_limit_error" {
				t.Fatalf("error.type=%s", got)
			}
			if !strings.Contains(ojson.Get(body, "error.message").String(), "(reset after 3723s)") {
				t.Fatalf("reset lost during Claude normalization: %s", body)
			}
		})
	}
}
