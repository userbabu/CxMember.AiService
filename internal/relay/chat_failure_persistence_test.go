package relay

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func failurePersistenceServer(t *testing.T, upstream http.HandlerFunc) (*Server, string, string, *atomic.Int32) {
	t.Helper()
	dsn := os.Getenv("AI_FAILURE_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated context test database not configured")
	}
	config, err := mysql.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(config.DBName, "relay_error_tests_") || config.Addr != "127.0.0.1:3306" {
		t.Fatal("explicit isolated test database required")
	}
	db, err := sql.Open("mysql", dsn)
	store := &Store{db: db}
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		upstream(w, r)
	}))
	s := NewServer(Config{EncryptionKey: "context-policy-test-key", UpstreamTimeout: 5 * time.Second}, store)
	uid := fmt.Sprint(930000 + walletTestUID.Add(1))
	model := "context-" + uid
	derived := sha256.Sum256([]byte(s.cfg.EncryptionKey))
	block, _ := aes.NewCipher(derived[:])
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	encrypted := append(nonce, gcm.Seal(nil, nonce, []byte("context-test-provider-secret"), nil)...)
	result, err := store.db.Exec(`INSERT INTO ai_provider(name,base_url,enabled) VALUES(?,?,1)`, model, provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	providerID, _ := result.LastInsertId()
	result, err = store.db.Exec(`INSERT INTO ai_provider_credential(provider_id,name,secret_value,enabled) VALUES(?,'test',?,1)`, providerID, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	credentialID, _ := result.LastInsertId()
	if _, err = store.db.Exec(`INSERT INTO ai_model(model_id,display_name,provider_id,credential_id,upstream_model,product_scope,input_price_micro_per_1k,output_price_micro_per_1k,max_output_tokens,allow_tools,enabled) VALUES(?,?,?,?,?,'17',1000,1000,4096,1,1)`, model, model, providerID, credentialID, model); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`INSERT INTO ai_account(uid) VALUES(?)`, `INSERT INTO ai_wallet(uid,balance_micro) VALUES(?,1000000000)`} {
		if _, err = store.db.Exec(query, uid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.db.Exec(`INSERT INTO ai_model_pricing(model_id) VALUES(?)`, model); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		provider.Close()
		for _, query := range []string{`DELETE FROM ai_usage_pricing WHERE uid=?`, `DELETE FROM ai_usage WHERE uid=?`, `DELETE FROM ai_wallet_ledger WHERE uid=?`, `DELETE FROM ai_account WHERE uid=?`} {
			if _, err := store.db.Exec(query, uid); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
		if _, err := store.db.Exec(`DELETE FROM ai_rate_window WHERE model_id=?`, model); err != nil {
			t.Error(err)
		}
		if _, err := store.db.Exec(`DELETE FROM ai_model WHERE model_id=?`, model); err != nil {
			t.Error(err)
		}
		if _, err := store.db.Exec(`DELETE FROM ai_provider_credential WHERE credential_id=?`, credentialID); err != nil {
			t.Error(err)
		}
		if _, err := store.db.Exec(`DELETE FROM ai_provider WHERE provider_id=?`, providerID); err != nil {
			t.Error(err)
		}
		store.Close()
	})
	var defaultLimit int
	if err = store.db.QueryRow(`SELECT max_context_tokens FROM ai_model WHERE model_id=?`, model).Scan(&defaultLimit); err != nil || defaultLimit != 65536 {
		t.Fatalf("initial context=%d err=%v", defaultLimit, err)
	}
	return s, uid, model, calls
}

type failurePersistRow struct {
	status, code, detail string
	charged, elapsed     int64
}

func readFailurePersisted(t *testing.T, s *Server, id string) failurePersistRow {
	t.Helper()
	var row failurePersistRow
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := s.store.db.QueryRow(`SELECT status,error_code,COALESCE(error_detail,''),charged_micro,COALESCE(elapsed_ms,0) FROM ai_usage WHERE request_id=?`, id).Scan(&row.status, &row.code, &row.detail, &row.charged, &row.elapsed)
		if err == nil {
			return row
		}
		if err != sql.ErrNoRows || time.Now().After(deadline) {
			t.Fatalf("failure row missing: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func assertFailureFunds(t *testing.T, s *Server, uid, id string) {
	t.Helper()
	assertBalance(t, s, uid, 1000000000)
	var sum int64
	var reserve, refund int
	err := s.store.db.QueryRow(`SELECT COALESCE(SUM(delta_micro),0),SUM(reason='reserve'),SUM(reason='release') FROM ai_wallet_ledger WHERE uid=? AND request_id=?`, uid, id).Scan(&sum, &reserve, &refund)
	if err != nil || sum != 0 || reserve != 1 || refund != 1 {
		t.Fatalf("financial failure sum=%d reserve=%d refund=%d err=%v", sum, reserve, refund, err)
	}
}
func TestChatFailurePersistUpstreamBodiesAndFunds(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		stream   bool
		body     string
		expected string
	}{{"http404", 404, false, `{"error":{"message":"isolated missing upstream route","api_key":"context-test-provider-secret"}}`, "isolated missing upstream route"}, {"json200error", 200, false, `{"error":{"message":"isolated JSON diagnostic","api_key":"context-test-provider-secret"}}`, "isolated JSON diagnostic"}, {"sseerror", 200, true, "data: {\"error\":{\"message\":\"isolated SSE diagnostic\",\"api_key\":\"context-test-provider-secret\"}}\n\n", "isolated SSE diagnostic"}} {
		t.Run(test.name, func(t *testing.T) {
			s, uid, model, calls := failurePersistenceServer(t, func(w http.ResponseWriter, r *http.Request) {
				if test.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			})
			authorization := accountTestAuthorization(t, s, uid)
			id := newRequestID()
			response := accountTestRequest(t, s, uid, model, authorization, id, test.stream)
			if calls.Load() != 1 {
				t.Fatalf("upstream calls=%d response=%s", calls.Load(), response.Body.String())
			}
			row := readFailurePersisted(t, s, id)
			if row.status != "failed" || row.charged != 0 || !strings.Contains(row.detail, test.expected) {
				t.Fatalf("unexpected failure persisted: %+v", row)
			}
			var detail map[string]any
			if err := json.Unmarshal([]byte(row.detail), &detail); err != nil {
				t.Fatal(err)
			}
			if detail["upstream_status"] != float64(test.status) || detail["provider"] == "" || detail["upstream_model"] != model || row.elapsed < 0 {
				t.Fatalf("incomplete error details %+v", detail)
			}
			if strings.Contains(row.detail, "context-test-provider-secret") {
				t.Fatal("provider credential persisted")
			}
			if strings.Contains(response.Body.String(), test.expected) {
				t.Fatal("internal diagnostic leaked to customer")
			}
			if response.Header().Get("X-Request-ID") != id {
				t.Fatal("request ID not preserved")
			}
			assertFailureFunds(t, s, uid, id)
		})
	}
}
func TestChatFailurePersistTimeoutAndFunds(t *testing.T) {
	s, uid, model, calls := failurePersistenceServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	s.client.Timeout = 40 * time.Millisecond
	authorization := accountTestAuthorization(t, s, uid)
	id := newRequestID()
	response := accountTestRequest(t, s, uid, model, authorization, id, false)
	row := readFailurePersisted(t, s, id)
	if row.code != "upstream_timeout" || row.status != "failed" || row.charged != 0 || calls.Load() != 1 || response.Code != 502 {
		t.Fatalf("timeout failure=%+v calls=%d http=%d", row, calls.Load(), response.Code)
	}
	if row.elapsed < 20 || row.elapsed > 2000 {
		t.Fatalf("elapsed_ms=%d", row.elapsed)
	}
	assertFailureFunds(t, s, uid, id)
}
func TestChatFailurePersistCancellationAndFunds(t *testing.T) {
	started := make(chan struct{})
	s, uid, model, _ := failurePersistenceServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	authorization := accountTestAuthorization(t, s, uid)
	relay := httptest.NewServer(s.Handler())
	defer relay.Close()
	id := newRequestID()
	info := &ederClientInfo{EderVersion: "isolated-1", SoftwareVersion: "isolated-1", SoftwareHash: strings.Repeat("a", 64), System: "isolated test"}
	raw, _ := json.Marshal(chatRequest{Model: model, Messages: []map[string]any{{"role": "user", "content": "hello"}}, MaxTokens: 8, ClientInfo: info})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, relay.URL+"/v1/chat/completions", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", authorization)
	req.Header.Set("X-Request-ID", id)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach upstream")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("client cancellation unexpectedly succeeded")
	}
	row := readFailurePersisted(t, s, id)
	if row.status != "failed" || row.code != "request_cancelled" || row.charged != 0 {
		t.Fatalf("cancelled failure=%+v", row)
	}
	assertFailureFunds(t, s, uid, id)
}
func TestChatFailureSuccessKeepsFinancialAndNoFailureRecord(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			s, uid, model, _ := failurePersistenceServer(t, func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"success sentinel\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"success sentinel"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
				}
			})
			authorization := accountTestAuthorization(t, s, uid)
			id := newRequestID()
			response := accountTestRequest(t, s, uid, model, authorization, id, stream)
			if response.Code != 200 || !strings.Contains(response.Body.String(), "success sentinel") {
				t.Fatalf("success response=%d %s", response.Code, response.Body.String())
			}
			row := readFailurePersisted(t, s, id)
			if row.status != "completed" || row.code != "" || row.detail != "" || row.charged != 5 {
				t.Fatalf("success persisted failure or wrong charge %+v", row)
			}
			var failed int
			if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_usage WHERE uid=? AND status='failed'`, uid).Scan(&failed); err != nil || failed != 0 {
				t.Fatalf("success failure records=%d err=%v", failed, err)
			}
			assertBalance(t, s, uid, 999999995)
		})
	}
}
func TestChatFailureSecretRedactionBoundaries(t *testing.T) {
	raw := `{"api_key":"custom-secret","password":"123456","cookie":"sensitive"} Bearer exposed-token sk-testcredential https://name:pass@example.com/`
	masked := redactDiagnostic(raw, "custom-secret")
	for _, value := range []string{"custom-secret", "123456", "sensitive", "exposed-token", "sk-testcredential", "name:pass"} {
		if strings.Contains(masked, value) {
			t.Errorf("secret retained %q", value)
		}
	}
	if !strings.Contains(masked, "[redacted]") {
		t.Fatal("no redaction")
	}
}

func assertUnbilledChatFailure(t *testing.T, s *Server, uid, requestID, code, message string, httpStatus int) {
	t.Helper()
	var chargeable int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_usage WHERE uid=? AND (status<>'failed' OR charged_micro<>0)`, uid).Scan(&chargeable); err != nil || chargeable != 0 {
		t.Fatalf("unexpected billed usage: count=%d err=%v", chargeable, err)
	}
	row := readFailurePersisted(t, s, requestID)
	if row.status != "failed" || row.charged != 0 || row.code != code {
		t.Fatalf("wrong unbilled failure: %+v", row)
	}
	var detail struct {
		Message string `json:"message"`
		Status  int    `json:"http_status"`
		Stage   string `json:"stage"`
	}
	if err := json.Unmarshal([]byte(row.detail), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Message != message || detail.Status != httpStatus || detail.Stage == "" {
		t.Fatalf("wrong failure reason: %+v want=%q HTTP=%d", detail, message, httpStatus)
	}
}

func TestChatFailurePersistRateQueueCancellation(t *testing.T) {
	s, uid, model, calls := failurePersistenceServer(t, func(http.ResponseWriter, *http.Request) { t.Error("rate-queued request reached upstream") })
	if _, err := s.store.db.Exec(`UPDATE ai_model SET rpm=1 WHERE model_id=?`, model); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`INSERT INTO ai_rate_window(uid,model_id,window_minute,request_count,token_count) VALUES(0,?,DATE_FORMAT(NOW(),'%Y-%m-%d %H:%i:00'),1,0),(0,?,DATE_ADD(DATE_FORMAT(NOW(),'%Y-%m-%d %H:%i:00'),INTERVAL 1 MINUTE),1,0)`, model, model); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(walletContext(uid, ""))
	defer cancel()
	raw, _ := json.Marshal(chatRequest{ClientInfo: isolatedClientInfo(), Model: model, Messages: []map[string]any{{"role": "user", "content": "hello"}}, MaxTokens: 8})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(raw))).WithContext(ctx)
	response := httptest.NewRecorder()
	timer := time.AfterFunc(200*time.Millisecond, cancel)
	defer timer.Stop()
	requestTrace(http.HandlerFunc(s.chat)).ServeHTTP(response, request)
	row := readFailurePersisted(t, s, response.Header().Get("X-Request-ID"))
	var detail map[string]any
	if err := json.Unmarshal([]byte(row.detail), &detail); err != nil {
		t.Fatal(err)
	}
	if row.code != "request_cancelled" || row.status != "failed" || row.charged != 0 || detail["stage"] != "queue_reservation" || !strings.Contains(fmt.Sprint(detail["cause"]), "context canceled") {
		t.Fatalf("rate queue cancellation not retained %+v", row)
	}
	assertBalance(t, s, uid, 1000000000)
	var count int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_wallet_ledger WHERE uid=?`, uid).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unreserved request changed ledger count=%d err=%v", count, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
}
