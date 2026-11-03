package relay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func accountTestAuthorization(t *testing.T, s *Server, uid string) string {
	t.Helper()
	s.cfg.JWTSecret = strings.Repeat("isolated-account-test-", 2)
	s.cfg.JWTIssuer = "cx-member"
	s.cfg.JWTAudience = "cx-ai-relay"
	now := time.Now().Unix()
	claims := Claims{Issuer: s.cfg.JWTIssuer, Audience: s.cfg.JWTAudience, Subject: uid, ParentUID: uid, Product: "17", TokenID: newRequestID(), IssuedAt: now, NotBefore: now - 1, Expires: now + 300}
	if _, err := s.store.db.Exec(`INSERT INTO ai_user_token(jti,uid,product,expires_at) VALUES(?,?,?,FROM_UNIXTIME(?))`, claims.TokenID, uid, "17", claims.Expires); err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	raw, _ := json.Marshal(claims)
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(s.cfg.JWTSecret))
	mac.Write([]byte(unsigned))
	return "Bearer " + unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func accountTestRequest(t *testing.T, s *Server, uid, model, authorization, id string, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	info := &ederClientInfo{EderVersion: "isolated-1", SoftwareVersion: "isolated-1", SoftwareHash: strings.Repeat("a", 64), System: "isolated test"}
	raw, _ := json.Marshal(chatRequest{Model: model, Messages: []map[string]any{{"role": "user", "content": "hello"}}, Stream: stream, MaxTokens: 8, ClientInfo: info})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(raw)))
	request.Header.Set("Authorization", authorization)
	request.Header.Set("X-Request-ID", id)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	return response
}

func assertAccountDisabledResponse(t *testing.T, response *httptest.ResponseRecorder, id, message string) {
	t.Helper()
	var body struct {
		Code      int    `json:"code"`
		Msg       string `json:"msg"`
		RequestID string `json:"request_id"`
		Error     struct {
			Type      string `json:"type"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusForbidden || body.Code != 403 || body.Error.Type != "account_disabled" || body.Msg != message || body.Error.Message != message || body.RequestID != id || body.Error.RequestID != id || response.Header().Get("X-Request-ID") != id {
		t.Fatalf("disabled response mismatch: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Content-Type"), "application/json") || strings.Contains(response.Body.String(), "data:") {
		t.Fatal("account rejection must be JSON before starting SSE")
	}
}

func assertAccountNoBilling(t *testing.T, s *Server, uid, model, id, message string) {
	t.Helper()
	assertBalance(t, s, uid, 1000000000)
	for _, table := range []string{"ai_wallet_ledger", "ai_usage_pricing"} {
		var count int
		if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE uid=?`, uid).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	assertUnbilledChatFailure(t, s, uid, id, "account_disabled", message, http.StatusForbidden)
	var count int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_rate_window WHERE model_id=?`, model).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rate window count=%d err=%v", count, err)
	}
}

func TestDisabledAccountRejectsAuthenticatedChatBeforeBilling(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, stored := range []string{"暂时无法对话", ""} {
			name := "json"
			if stream {
				name = "stream"
			}
			if stored == "" {
				name += "/default"
			} else {
				name += "/configured"
			}
			t.Run(name, func(t *testing.T) {
				s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("disabled account reached upstream") })
				authorization := accountTestAuthorization(t, s, uid)
				if _, err := s.store.db.Exec(`UPDATE ai_account SET status='suspended',disabled_message=? WHERE uid=?`, stored, uid); err != nil {
					t.Fatal(err)
				}
				id := newRequestID()
				response := accountTestRequest(t, s, uid, model, authorization, id, stream)
				want := stored
				if want == "" {
					want = "该账户已被禁用，暂时无法继续对话。"
				}
				assertAccountDisabledResponse(t, response, id, "您的账户异常："+want)
				assertAccountNoBilling(t, s, uid, model, id, "您的账户异常："+want)
				if calls.Load() != 0 {
					t.Fatal("disabled account called upstream")
				}
			})
		}
	}
}

func TestActiveAccountCompletesJSONAndStreamingChat(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "json"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			s, uid, model, calls := contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"isolated ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"isolated ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
				}
			})
			authorization := accountTestAuthorization(t, s, uid)
			id := newRequestID()
			response := accountTestRequest(t, s, uid, model, authorization, id, stream)
			if response.Code != 200 || !strings.Contains(response.Body.String(), "isolated ok") || response.Header().Get("X-Request-ID") != id {
				t.Fatalf("active chat failed: %d %s", response.Code, response.Body.String())
			}
			if stream && !strings.Contains(response.Body.String(), "data: [DONE]") {
				t.Fatal("SSE did not complete")
			}
			if calls.Load() != 1 {
				t.Fatalf("upstream calls=%d", calls.Load())
			}
			assertBalance(t, s, uid, 999999995)
			var status string
			if err := s.store.db.QueryRow(`SELECT status FROM ai_usage WHERE uid=?`, uid).Scan(&status); err != nil || status != "completed" {
				t.Fatalf("settlement status=%s err=%v", status, err)
			}
		})
	}
}

func TestStreamingErrorsKeepUnifiedTraceAndMessage(t *testing.T) {
	response := httptest.NewRecorder()
	id := newRequestID()
	response.Header().Set("X-Request-ID", id)
	_, _, _, err := proxySSE(response, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"error\":{\"message\":\"upstream internal detail\"}}\n\n"))})
	if err == nil {
		t.Fatal("upstream error accepted")
	}
	frame := strings.TrimSpace(strings.TrimPrefix(response.Body.String(), "data: "))
	var body struct {
		Code      int    `json:"code"`
		Msg       string `json:"msg"`
		RequestID string `json:"request_id"`
		Error     struct {
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err = json.Unmarshal([]byte(frame), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != 502 || body.Msg == "" || body.Msg != body.Error.Message || body.RequestID != id || body.Error.RequestID != id || strings.Contains(frame, "upstream internal detail") {
		t.Fatalf("stream error envelope invalid: %s", frame)
	}
}

// 仅拦截真实MySQL模型查询的返回时机，让停用在预扣之前并发提交。
type accountTransitionConnector struct {
	inner    driver.Connector
	observed chan struct{}
	proceed  chan struct{}
	once     sync.Once
}

func (c *accountTransitionConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &accountTransitionConn{Conn: conn, owner: c}, nil
}
func (c *accountTransitionConnector) Driver() driver.Driver { return c.inner.Driver() }

type accountTransitionConn struct {
	driver.Conn
	owner *accountTransitionConnector
}

func (c *accountTransitionConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *accountTransitionConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err == nil && strings.HasPrefix(query, "SELECT p.name,p.base_url,m.upstream_model") {
		c.owner.once.Do(func() {
			close(c.owner.observed)
			select {
			case <-c.owner.proceed:
			case <-ctx.Done():
			}
		})
	}
	return rows, err
}

func TestAccountSuspendedAfterInitialCheckStillRejectsBeforeReservation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "json"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("late-disabled account called upstream") })
			authorization := accountTestAuthorization(t, s, uid)
			baseStore := s.store
			config, err := mysql.ParseDSN(os.Getenv("AI_CONTEXT_TEST_DSN"))
			if err != nil {
				t.Fatal(err)
			}
			config.InterpolateParams = true
			inner, err := mysql.NewConnector(config)
			if err != nil {
				t.Fatal(err)
			}
			observe := &accountTransitionConnector{inner: inner, observed: make(chan struct{}), proceed: make(chan struct{})}
			instrumented := sql.OpenDB(observe)
			defer instrumented.Close()
			s.store = &Store{db: instrumented}
			var release sync.Once
			defer release.Do(func() { close(observe.proceed) })
			id := newRequestID()
			complete := make(chan *httptest.ResponseRecorder, 1)
			go func() { complete <- accountTestRequest(t, s, uid, model, authorization, id, stream) }()
			select {
			case <-observe.observed:
			case <-time.After(3 * time.Second):
				t.Fatal("model query not observed")
			}
			if _, err := baseStore.db.Exec(`UPDATE ai_account SET status='suspended',disabled_message='排查中的禁用说明' WHERE uid=?`, uid); err != nil {
				t.Fatal(err)
			}
			release.Do(func() { close(observe.proceed) })
			var response *httptest.ResponseRecorder
			select {
			case response = <-complete:
			case <-time.After(3 * time.Second):
				t.Fatal("late-disabled chat did not complete")
			}
			assertAccountDisabledResponse(t, response, id, "您的账户异常：排查中的禁用说明")
			assertAccountNoBilling(t, s, uid, model, id, "您的账户异常：排查中的禁用说明")
			if calls.Load() != 0 {
				t.Fatal("late-disabled account called upstream")
			}
		})
	}
}
