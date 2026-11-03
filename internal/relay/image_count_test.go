package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

func imageCountMessages(t *testing.T, count int) []map[string]any {
	t.Helper()
	result := []map[string]any{}
	for i := 0; i < count; i++ {
		result = append(result, testImageMessages(testImageURL(t, "png", 8, 8))...)
	}
	return result
}

func TestModelImageCountPolicyBoundsAndDefault(t *testing.T) {
	for _, n := range []int{-1, 0, 33, 255} {
		if validModelImageCount(n) {
			t.Errorf("invalid count accepted %d", n)
		}
	}
	for _, n := range []int{1, 4, 32} {
		if !validModelImageCount(n) {
			t.Errorf("valid count rejected %d", n)
		}
	}
	for _, base := range []string{"", "https://api.deepseek.com"} {
		if p := modelImageInputPolicy(base, "deepseek-flash"); p.maxImages != 4 {
			t.Fatal("default must be 4")
		}
	}
	for _, maximum := range []int{1, 4, 32} {
		t.Run(fmt.Sprint(maximum), func(t *testing.T) {
			p := modelImageInputPolicy("", "")
			p.maxImages = maximum
			messages := imageCountMessages(t, maximum)
			before, _ := json.Marshal(messages)
			if _, err := chatInputEstimate(messages, nil, nil, true, p); err != nil {
				t.Fatal("exact limit rejected", err)
			}
			after, _ := json.Marshal(messages)
			if string(before) != string(after) {
				t.Fatal("image messages altered")
			}
			messages = append(messages, imageCountMessages(t, 1)...)
			if _, err := chatInputEstimate(messages, nil, nil, true, p); err == nil || !strings.Contains(err.Error(), "image count exceeds model request limit") {
				t.Fatal("overflow did not reject count", err)
			}
		})
	}
	p := modelImageInputPolicy("", "")
	p.maxImages = 0
	if _, err := chatInputEstimate(nil, nil, nil, false, p); err == nil {
		t.Fatal("invalid policy accepted even for text")
	}
}

func TestModelImageCountCatalogAndQuotaReload(t *testing.T) {
	s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("catalog reached upstream") })
	var configured int
	if err := s.store.db.QueryRow(`SELECT max_images_per_request FROM ai_model WHERE model_id=?`, model).Scan(&configured); err != nil || configured != 4 {
		t.Fatalf("new model image count default=%d error=%v", configured, err)
	}
	quotaAddLedger(t, s, uid, "admin_recharge", 1000000000)
	for _, n := range []int{4, 1, 32} {
		if _, err := s.store.db.Exec(`UPDATE ai_model SET max_images_per_request=? WHERE model_id=?`, n, model); err != nil {
			t.Fatal(err)
		}
		for _, quota := range []bool{false, true} {
			if item := defaultCatalogItem(t, s, uid, model, quota); item["max_images_per_request"] != float64(n) {
				t.Fatalf("catalog count=%v want=%d", item["max_images_per_request"], n)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("catalog contacted upstream")
	}
}

func TestModelImageCountHTTPHistoryToolsAndNoReservation(t *testing.T) {
	for _, maximum := range []int{1, 4, 32} {
		t.Run(fmt.Sprint(maximum), func(t *testing.T) {
			var forwarded []map[string]any
			s, uid, model, calls := contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				forwarded = body.Messages
				w.Write([]byte(`{"choices":[{"message":{"content":"seen"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9000,"completion_tokens":2}}`))
			})
			if _, err := s.store.db.Exec(`UPDATE ai_model SET allow_images=1,max_images_per_request=?,max_context_tokens=1048576 WHERE model_id=?`, maximum, model); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.db.Exec(`UPDATE ai_model_pricing SET vision_input_price_micro_per_1k=1000,vision_output_price_micro_per_1k=1000 WHERE model_id=?`, model); err != nil {
				t.Fatal(err)
			}
			messages := imageCountMessages(t, maximum)
			messages = append(messages, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "count-tool", "type": "function", "function": map[string]any{"name": "check", "arguments": "{}"}}}}, map[string]any{"role": "tool", "tool_call_id": "count-tool", "content": "done"})
			over := append(messages, imageCountMessages(t, 1)...)
			payload := chatRequest{Model: model, Messages: over, ClientInfo: isolatedClientInfo(), MaxTokens: 32}
			w := globalPromptChat(t, s, uid, payload)
			if w.Code != 400 || calls.Load() != 0 || !strings.Contains(w.Body.String(), "image count exceeds model request limit") {
				t.Fatalf("overflow status=%d upstream=%d body=%s", w.Code, calls.Load(), w.Body.String())
			}
			assertBalance(t, s, uid, 1000000000)
			var ledgers int
			if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_wallet_ledger WHERE uid=?`, uid).Scan(&ledgers); err != nil || ledgers != 0 {
				t.Fatalf("overflow wrote ledger %d %v", ledgers, err)
			}
			payload.Messages = messages
			w = globalPromptChat(t, s, uid, payload)
			if w.Code != 200 || calls.Load() != 1 {
				t.Fatalf("exact count status=%d upstream=%d body=%s", w.Code, calls.Load(), w.Body.String())
			}
			// 系统提示词会增加一条message；所有原图片及工具回执必须原样保留。
			if len(forwarded) < len(messages) || !reflect.DeepEqual(forwarded[len(forwarded)-len(messages):], messages) {
				t.Fatal("forwarding changed history images or tool messages")
			}
			assertBalance(t, s, uid, 1000000000-9002)
		})
	}
}

func TestModelImageCountInvalidConfigurationFailsClosed(t *testing.T) {
	s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("bad config reached upstream") })
	for _, n := range []int{0, 33, 255} {
		if _, err := s.store.db.Exec(`UPDATE ai_model SET max_images_per_request=? WHERE model_id=?`, n, model); err != nil {
			t.Fatal(err)
		}
		if _, err := s.publicModelPrices(walletContext(uid, ""), "17"); err == nil {
			t.Fatal("catalog accepted invalid count", n)
		}
		w := globalPromptChat(t, s, uid, chatRequest{Model: model, Messages: imageCountMessages(t, 1), ClientInfo: isolatedClientInfo(), MaxTokens: 32})
		if w.Code != 503 {
			t.Fatalf("bad count status=%d body=%s", w.Code, w.Body.String())
		}
		assertBalance(t, s, uid, 1000000000)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid count reached upstream")
	}
}

func TestModelImageCountStartupRequiresMigration(t *testing.T) {
	dsn := os.Getenv("AI_WALLET_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated database not configured")
	}
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.db.Exec(`ALTER TABLE ai_model RENAME COLUMN max_images_per_request TO test_hidden_image_count`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := store.db.Exec(`ALTER TABLE ai_model RENAME COLUMN test_hidden_image_count TO max_images_per_request`); err != nil {
			t.Error(err)
		}
	}()
	opened, err := OpenStore(dsn)
	if opened != nil {
		opened.Close()
		t.Fatal("missing 022 accepted")
	}
	if err == nil || !strings.Contains(err.Error(), "022") {
		t.Fatal("wrong startup failure", err)
	}
}
