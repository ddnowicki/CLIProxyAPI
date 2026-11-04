package claude

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

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
