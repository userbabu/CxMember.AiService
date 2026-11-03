package relay

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func isolatedClientInfo() *ederClientInfo {
	return &ederClientInfo{EderVersion: "isolated-1", SoftwareVersion: "isolated-1", SoftwareHash: strings.Repeat("a", 64), System: "isolated test"}
}

func testSuccessData(t *testing.T, raw []byte) json.RawMessage {
	t.Helper()
	var envelope struct {
		Code   int             `json:"code"`
		Msg    string          `json:"msg"`
		Data   json.RawMessage `json:"data"`
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != 0 || envelope.Msg != "操作成功" || string(envelope.Errors) != "null" || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		t.Fatalf("invalid success envelope: %s", raw)
	}
	return envelope.Data
}

func assertFailureTrace(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		RequestID string `json:"request_id"`
		Error     struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	id := response.Header().Get("X-Request-ID")
	if id == "" || body.RequestID != id || body.Error.RequestID != id {
		t.Fatalf("failure trace mismatch: %s", response.Body.String())
	}
}

func TestModelContextTiers(t *testing.T) {
	want := []int{4096, 8192, 16384, 32768, 65536, 131072, 262144, 524288, 1048576}
	for i, limit := range want {
		if !validContextLimit(limit) {
			t.Fatalf("valid tier rejected: %d", limit)
		}
		if got := contextTiers(limit); !reflect.DeepEqual(got, want[:i+1]) {
			t.Fatalf("limit=%d tiers=%v", limit, got)
		}
	}
	for _, invalid := range []int{-1, 0, 4095, 4097, 1000000, 1048577} {
		if validContextLimit(invalid) {
			t.Fatalf("invalid tier accepted: %d", invalid)
		}
	}
	tiers := contextTiers(65536)
	tiers[0] = 0
	if contextTiers(65536)[0] != 4096 {
		t.Fatal("caller mutated shared tiers")
	}
}

// 只使用显式指定的独立测试库，每个用例创建自己的模型与钱包并在结束后清理。
func contextPolicyServer(t *testing.T, upstream http.HandlerFunc) (*Server, string, string, *atomic.Int32) {
	t.Helper()
	if os.Getenv("AI_FAILURE_TEST_DSN") != "" {
		return failurePersistenceServer(t, upstream)
	}
	dsn := os.Getenv("AI_CONTEXT_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated context test database not configured")
	}
	store, err := OpenStore(dsn)
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

func TestContextPolicyCatalogAndQuota(t *testing.T) {
	s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("catalog called upstream") })
	quotaAddLedger(t, s, uid, "admin_recharge", 1000000000)
	want := []int{4096, 8192, 16384, 32768, 65536, 131072, 262144, 524288, 1048576}
	for i, limit := range want {
		if _, err := s.store.db.Exec(`UPDATE ai_model SET max_context_tokens=? WHERE model_id=?`, limit, model); err != nil {
			t.Fatal(err)
		}
		for _, endpoint := range []string{"models", "quota"} {
			request := httptest.NewRequest(http.MethodGet, "/v1/"+endpoint, nil).WithContext(walletContext(uid, ""))
			response := httptest.NewRecorder()
			if endpoint == "models" {
				s.models(response, request)
			} else {
				s.quota(response, request)
			}
			if response.Code != 200 {
				t.Fatalf("%s status=%d: %s", endpoint, response.Code, response.Body.String())
			}
			var payload struct {
				Data []struct {
					ID    string `json:"id"`
					Limit int    `json:"max_context_tokens"`
					Tiers []int  `json:"context_tiers"`
				} `json:"data"`
				PricingModels []struct {
					ID    string `json:"id"`
					Limit int    `json:"max_context_tokens"`
					Tiers []int  `json:"context_tiers"`
				} `json:"pricing_models"`
			}
			if err := json.Unmarshal(testSuccessData(t, response.Body.Bytes()), &payload); err != nil {
				t.Fatal(err)
			}
			found := false
			if endpoint == "models" {
				for _, item := range payload.Data {
					if item.ID == model {
						found = true
						if item.Limit != limit || !reflect.DeepEqual(item.Tiers, want[:i+1]) {
							t.Fatalf("bad model policy: %+v", item)
						}
					}
				}
			} else {
				for _, item := range payload.PricingModels {
					if item.ID == model {
						found = true
						if item.Limit != limit || !reflect.DeepEqual(item.Tiers, want[:i+1]) {
							t.Fatalf("bad quota model policy: %+v", item)
						}
					}
				}
			}
			if !found {
				t.Fatal("available model missing")
			}
			if strings.Contains(response.Body.String(), "secret_value") || strings.Contains(response.Body.String(), "provider-secret") {
				t.Fatal("credential leaked")
			}
		}
	}
	forbidden, err := s.publicModelPrices(context.Background(), "other-product")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range forbidden {
		if item["id"] == model {
			t.Fatal("cross-product model policy exposed")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("catalog unexpectedly called upstream")
	}
}

func TestContextPolicyRejectsPromptBeforeBilling(t *testing.T) {
	s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("oversize prompt reached upstream") })
	if _, err := s.store.db.Exec(`UPDATE ai_model SET max_context_tokens=4096 WHERE model_id=?`, model); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, content string
		tools         json.RawMessage
	}{
		{"messages", strings.Repeat("x", 13000), nil},
		{"exact_context_boundary", strings.Repeat("x", 12187), nil},
		{"tool_schema", "hello", json.RawMessage(`[{"type":"function","function":{"name":"sample","description":"` + strings.Repeat("x", 13000) + `","parameters":{"type":"object"}}}]`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(chatRequest{ClientInfo: isolatedClientInfo(), Model: model, Messages: []map[string]any{{"role": "user", "content": tc.content}}, Tools: tc.tools, MaxTokens: 1})
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))).WithContext(walletContext(uid, ""))
			response := httptest.NewRecorder()
			requestTrace(http.HandlerFunc(s.chat)).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertBalance(t, s, uid, 1000000000)
			for _, table := range []string{"ai_wallet_ledger", "ai_usage_pricing"} {
				var count int
				if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE uid=?`, uid).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s rows=%d err=%v", table, count, err)
				}
			}
			assertUnbilledChatFailure(t, s, uid, response.Header().Get("X-Request-ID"), "invalid_request_error", "estimated prompt size exceeds this model's context limit", http.StatusBadRequest)
			assertFailureTrace(t, response)
		})
	}
	if calls.Load() != 0 {
		t.Fatal("rejected prompt called upstream")
	}
}

func TestContextPolicyClampsOutputAndRespectsTPM(t *testing.T) {
	for _, tc := range []struct {
		name                                          string
		maxContext, tpm, contentBytes, expectedOutput int
		completionField                               bool
	}{
		{"context_remaining", 4096, 0, 12000, 63, false},
		{"one_output_token_remaining", 4096, 0, 12184, 1, false},
		{"completion_field", 4096, 0, 12000, 63, true},
		{"tpm_remaining", 4096, 4050, 12000, 17, false},
		{"one_million_window", 1048576, 0, 3000000, 4096, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var actualOutput int
			s, uid, model, calls := contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					MaxTokens int `json:"max_tokens"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				actualOutput = input.MaxTokens
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":1}}`))
			})
			if _, err := s.store.db.Exec(`UPDATE ai_model SET max_context_tokens=?,tpm=? WHERE model_id=?`, tc.maxContext, tc.tpm, model); err != nil {
				t.Fatal(err)
			}
			payload := chatRequest{ClientInfo: isolatedClientInfo(), Model: model, Messages: []map[string]any{{"role": "user", "content": strings.Repeat("x", tc.contentBytes)}}}
			if tc.completionField {
				payload.MaxCompletionTokens = 4096
			} else {
				payload.MaxTokens = 4096
			}
			body, _ := json.Marshal(payload)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))).WithContext(walletContext(uid, ""))
			response := httptest.NewRecorder()
			requestTrace(http.HandlerFunc(s.chat)).ServeHTTP(response, request)
			if response.Code != 200 {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if actualOutput != tc.expectedOutput {
				t.Fatalf("upstream output=%d want=%d", actualOutput, tc.expectedOutput)
			}
			if calls.Load() != 1 {
				t.Fatalf("upstream requests=%d", calls.Load())
			}
			var status string
			if err := s.store.db.QueryRow(`SELECT status FROM ai_usage WHERE uid=?`, uid).Scan(&status); err != nil || status != "completed" {
				t.Fatalf("usage status=%s err=%v", status, err)
			}
		})
	}
}

func TestContextPolicyStorageRejectsInvalidTiers(t *testing.T) {
	s, _, model, _ := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("storage validation called upstream") })
	for _, value := range []int{0, 4095, 4097, 1000000, 1048577} {
		if _, err := s.store.db.Exec(`UPDATE ai_model SET max_context_tokens=? WHERE model_id=?`, value, model); err == nil {
			t.Fatalf("database accepted invalid context tier %d", value)
		}
		var actual int
		if err := s.store.db.QueryRow(`SELECT max_context_tokens FROM ai_model WHERE model_id=?`, model).Scan(&actual); err != nil || actual != 65536 {
			t.Fatalf("failed update changed context=%d err=%v", actual, err)
		}
	}
}
