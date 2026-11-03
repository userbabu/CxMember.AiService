package relay

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// 只对显式提供的隔离测试库运行，验证钱包、用量与价格快照的事务边界。
func TestPricingSettlementTransaction(t *testing.T) {
	dsn := os.Getenv("AI_PRICING_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated pricing test database not configured")
	}
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	defer cleanupPricingIdentity(t, store, "42")
	ctx := context.Background()
	for _, query := range []string{"INSERT INTO ai_account(uid) VALUES(42)", "INSERT INTO ai_wallet(uid,balance_micro) VALUES(42,1000)"} {
		if _, err = store.db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	s := NewServer(Config{}, store)
	rate := billingRate{kind: "peak", timeZone: "Asia/Shanghai", lockedAt: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC), input: 2000, output: 8000, multiplier: 15000}
	requestID := "00000000-0000-4000-8000-000000000001"
	if err = s.reserve(ctx, "42", requestID, "test-model", 100, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.settle(ctx, "42", requestID, "test-model", 100, 200, 100, 60, rate); err != nil {
		t.Fatal(err)
	}
	var balance, input, output int64
	var kind string
	var multiplier uint64
	if err = store.db.QueryRow("SELECT balance_micro FROM ai_wallet WHERE uid=42").Scan(&balance); err != nil || balance != 940 {
		t.Fatalf("wallet=%d err=%v", balance, err)
	}
	if err = store.db.QueryRow("SELECT price_kind,input_price_micro_per_1k,output_price_micro_per_1k,multiplier_bps FROM ai_usage_pricing WHERE request_id=?", requestID).Scan(&kind, &input, &output, &multiplier); err != nil || kind != "peak" || input != 2000 || output != 8000 || multiplier != 15000 {
		t.Fatalf("snapshot mismatch: %v", err)
	}
	conflict := "00000000-0000-4000-8000-000000000002"
	if _, err = store.db.Exec("INSERT INTO ai_usage_pricing(request_id,uid,model_id,price_kind,time_zone,price_locked_at,input_price_micro_per_1k,output_price_micro_per_1k,multiplier_bps) SELECT ?,uid,model_id,price_kind,time_zone,price_locked_at,input_price_micro_per_1k,output_price_micro_per_1k,multiplier_bps FROM ai_usage_pricing WHERE request_id=?", conflict, requestID); err != nil {
		t.Fatal(err)
	}
	if err = s.reserve(ctx, "42", conflict, "test-model", 100, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.settle(ctx, "42", conflict, "test-model", 100, 200, 100, 60, rate); err == nil {
		t.Fatal("snapshot collision must fail")
	}
	var count int
	if err = store.db.QueryRow("SELECT COUNT(*) FROM ai_usage WHERE request_id=?", conflict).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed settlement left a usage record")
	}
	if err = store.db.QueryRow("SELECT balance_micro FROM ai_wallet WHERE uid=42").Scan(&balance); err != nil || balance != 840 {
		t.Fatal("failed settlement changed wallet")
	}
}

func TestChatLocksActivityPriceBeforeUpstream(t *testing.T) {
	dsn := os.Getenv("AI_PRICING_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated pricing test database not configured")
	}
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	defer cleanupPricingIdentity(t, store, "43")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 在请求执行过程中改价，正在执行的请求必须继续使用原来的活动价。
		if _, err := store.db.Exec("UPDATE ai_model_pricing SET limited_input_price_micro_per_1k=900000 WHERE model_id='test-model'"); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":200}}`))
	}))
	defer upstream.Close()
	key := "pricing-test-key"
	derived := sha256.Sum256([]byte(key))
	block, _ := aes.NewCipher(derived[:])
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	encrypted := append(nonce, gcm.Seal(nil, nonce, []byte("test-provider-secret"), nil)...)
	for _, query := range []string{"INSERT INTO ai_account(uid) VALUES(43)", "INSERT INTO ai_wallet(uid,balance_micro) VALUES(43,100000)", "UPDATE ai_model SET enabled=1 WHERE model_id='test-model'"} {
		if _, err = store.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.db.Exec("UPDATE ai_provider SET base_url=? WHERE provider_id=1", upstream.URL); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec("UPDATE ai_provider_credential SET secret_value=? WHERE credential_id=1", encrypted); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec("UPDATE ai_model_pricing SET limited_enabled=1,limited_start_unix=?,limited_end_unix=?,limited_input_price_micro_per_1k=500,limited_output_price_micro_per_1k=2000,limited_multiplier_bps=5000 WHERE model_id='test-model'", time.Now().Unix()-60, time.Now().Unix()+60); err != nil {
		t.Fatal(err)
	}
	s := NewServer(Config{EncryptionKey: key, UpstreamTimeout: time.Second * 5}, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}],"max_tokens":1024,"client_info":{"eder_version":"isolated-1","software_version":"isolated-1","software_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","system":"isolated test"}}`))
	request = request.WithContext(context.WithValue(request.Context(), memberKey{}, Claims{Subject: "43", Product: "17"}))
	response := httptest.NewRecorder()
	requestTrace(http.HandlerFunc(s.chat)).ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("chat failed: %d %s", response.Code, response.Body.String())
	}
	var balance, input, charge int64
	var kind string
	if err = store.db.QueryRow("SELECT balance_micro FROM ai_wallet WHERE uid=43").Scan(&balance); err != nil || balance != 99775 {
		t.Fatalf("activity wallet=%d err=%v", balance, err)
	}
	if err = store.db.QueryRow("SELECT p.price_kind,p.input_price_micro_per_1k,u.charged_micro FROM ai_usage_pricing p JOIN ai_usage u ON u.request_id=p.request_id WHERE p.uid=43").Scan(&kind, &input, &charge); err != nil || kind != "limited" || input != 500 || charge != 225 {
		t.Fatalf("activity snapshot=%s/%d/%d err=%v", kind, input, charge, err)
	}
}

func cleanupPricingIdentity(t *testing.T, store *Store, uid string) {
	t.Helper()
	for _, query := range []string{`DELETE FROM ai_usage_pricing WHERE uid=?`, `DELETE FROM ai_usage WHERE uid=?`, `DELETE FROM ai_wallet_ledger WHERE uid=?`, `DELETE FROM ai_account WHERE uid=?`} {
		if _, err := store.db.Exec(query, uid); err != nil {
			t.Errorf("pricing fixture cleanup: %v", err)
		}
	}
}
