package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

func sizedImageURL(t *testing.T, size int) string {
	t.Helper()
	data := mustImageBytes(t, testImageURL(t, "png", 8, 8))
	if size < len(data) {
		t.Fatal("test image size too small")
	}
	data = append(data, make([]byte, size-len(data))...)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

func TestModelImageLimitPolicyBounds(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 1023, 1025, 4194305, (32 << 20) + 1024} {
		if validModelImageLimit(n) {
			t.Errorf("invalid configured size accepted: %d", n)
		}
	}
	for _, n := range []int{1024, 2048, 4 << 20, 8 << 20, 32 << 20} {
		if !validModelImageLimit(n) {
			t.Errorf("valid configured size rejected: %d", n)
		}
	}
	for _, base := range []string{"", "https://api.deepseek.com"} {
		p := modelImageInputPolicy(base, "deepseek-flash")
		if p.maxFileBytes != 4<<20 {
			t.Fatal("new models must default to 4 MiB")
		}
	}
}

func TestModelImageLimitExactDecodedBoundary(t *testing.T) {
	p := modelImageInputPolicy("", "")
	p.maxFileBytes = 1024
	for _, tc := range []struct {
		size int
		ok   bool
	}{{1023, true}, {1024, true}, {1025, false}, {1026, false}, {1027, false}} {
		_, err := chatInputEstimate(testImageMessages(sizedImageURL(t, tc.size)), nil, nil, true, p)
		if (err == nil) != tc.ok {
			t.Errorf("decoded bytes=%d error=%v", tc.size, err)
		}
		if !tc.ok && (err == nil || !strings.Contains(err.Error(), "model file size")) {
			t.Fatal("wrong file-limit error", err)
		}
	}
	p.maxFileBytes = 0
	if _, err := chatInputEstimate(testImageMessages(testImageURL(t, "png", 8, 8)), nil, nil, true, p); err == nil {
		t.Fatal("invalid policy accepted")
	}
}

func TestModelImageLimitExpandedFileAndHistoryBound(t *testing.T) {
	p := modelImageInputPolicy("", "")
	p.maxFileBytes = 32 << 20
	large := sizedImageURL(t, 16<<20)
	messages := append(testImageMessages(large), testImageMessages(large)...)
	if _, err := chatInputEstimate(messages, nil, nil, true, p); err != nil {
		t.Fatal("exact aggregate boundary", err)
	}
	messages = append(messages, testImageMessages(testImageURL(t, "png", 8, 8))...)
	if _, err := chatInputEstimate(messages, nil, nil, true, p); err == nil || !strings.Contains(err.Error(), "total limit") {
		t.Fatal("aggregate bytes exceeded", err)
	}
	p.maxFileBytes = 4 << 20
	if _, err := chatInputEstimate(testImageMessages(large), nil, nil, true, p); err == nil {
		t.Fatal("model-specific file cap ignored")
	}
}

func TestModelImageLimitCatalogAndQuotaReload(t *testing.T) {
	s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("catalog called provider") })
	quotaAddLedger(t, s, uid, "admin_recharge", 1000000000)
	for _, size := range []int{1024, 4 << 20, 32 << 20} {
		if _, err := s.store.db.Exec(`UPDATE ai_model SET max_image_bytes=? WHERE model_id=?`, size, model); err != nil {
			t.Fatal(err)
		}
		for _, quota := range []bool{false, true} {
			item := defaultCatalogItem(t, s, uid, model, quota)
			if item["max_image_bytes"] != float64(size) {
				t.Errorf("catalog size=%v want=%d", item["max_image_bytes"], size)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("catalog called provider")
	}
}

func TestModelImageLimitChatPrecedesReservationAndSupportsLargeBody(t *testing.T) {
	var received int
	s, uid, model, calls := contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
		var data struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Error(err)
		}
		parts := data.Messages[len(data.Messages)-1]["content"].([]any)
		u := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
		received = len(mustImageBytes(t, u))
		w.Write([]byte(`{"choices":[{"message":{"content":"seen"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9000,"completion_tokens":2}}`))
	})
	if _, err := s.store.db.Exec(`UPDATE ai_model SET allow_images=1,max_image_bytes=1024 WHERE model_id=?`, model); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`UPDATE ai_model_pricing SET vision_input_price_micro_per_1k=1000,vision_output_price_micro_per_1k=1000 WHERE model_id=?`, model); err != nil {
		t.Fatal(err)
	}
	payload := chatRequest{Model: model, Messages: testImageMessages(sizedImageURL(t, 1025)), ClientInfo: isolatedClientInfo(), MaxTokens: 32}
	w := globalPromptChat(t, s, uid, payload)
	if w.Code != 400 || calls.Load() != 0 {
		t.Fatalf("oversized status=%d upstream=%d", w.Code, calls.Load())
	}
	assertBalance(t, s, uid, 1000000000)
	var ledger int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_wallet_ledger WHERE uid=?`, uid).Scan(&ledger); err != nil || ledger != 0 {
		t.Fatalf("oversized wrote ledger %d %v", ledger, err)
	}
	if _, err := s.store.db.Exec(`UPDATE ai_model SET max_image_bytes=? WHERE model_id=?`, 32<<20, model); err != nil {
		t.Fatal(err)
	}
	payload.Messages = testImageMessages(sizedImageURL(t, 13<<20))
	raw, _ := json.Marshal(payload)
	if len(raw) <= 16<<20 {
		t.Fatal("test body too small")
	}
	w = globalPromptChat(t, s, uid, payload)
	if w.Code != 200 || calls.Load() != 1 || received != 13<<20 {
		t.Fatalf("large image failed status=%d received=%d calls=%d body=%s", w.Code, received, calls.Load(), w.Body.String())
	}
	assertBalance(t, s, uid, 1000000000-9002)
}

func TestModelImageLimitInvalidConfigurationFailsClosed(t *testing.T) {
	s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("bad config called provider") })
	for _, size := range []int{0, 1025, (32 << 20) + 1024} {
		if _, err := s.store.db.Exec(`UPDATE ai_model SET max_image_bytes=? WHERE model_id=?`, size, model); err != nil {
			t.Fatal(err)
		}
		if _, err := s.publicModelPrices(walletContext(uid, ""), "17"); err == nil {
			t.Fatal("catalog accepted invalid size", size)
		}
		w := globalPromptChat(t, s, uid, chatRequest{Model: model, Messages: []map[string]any{{"role": "user", "content": "hello"}}, ClientInfo: isolatedClientInfo(), MaxTokens: 32})
		if w.Code != 503 {
			t.Fatalf("bad config status=%d", w.Code)
		}
		assertBalance(t, s, uid, 1000000000)
	}
	if calls.Load() != 0 {
		t.Fatal("bad config reached upstream")
	}
}

func TestModelImageLimitStartupRequiresMigration(t *testing.T) {
	dsn := os.Getenv("AI_WALLET_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated wallet test database not configured")
	}
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err = store.db.Exec(`ALTER TABLE ai_model RENAME COLUMN max_image_bytes TO test_hidden_image_bytes`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := store.db.Exec(`ALTER TABLE ai_model RENAME COLUMN test_hidden_image_bytes TO max_image_bytes`); err != nil {
			t.Error(err)
		}
	})
	opened, err := OpenStore(dsn)
	if opened != nil {
		opened.Close()
		t.Fatal("missing image size column accepted")
	}
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("021")) {
		t.Fatal("wrong startup error", err)
	}
}
