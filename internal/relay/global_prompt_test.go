package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestGlobalPromptPriorityAndMessageIntegrity(t *testing.T) {
	input := []map[string]any{
		{"role": "system", "content": "client system", "name": "client"},
		{"role": "developer", "content": []any{map[string]any{"type": "text", "text": "client developer"}}},
		{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.invalid/image.png"}}}},
		{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}},
		{"role": "tool", "tool_call_id": "call-1", "content": "result"},
	}
	before, _ := json.Marshal(input)
	prompt := "  所有对话规则\n保留格式与 emoji 😀  "
	for round := 0; round < 3; round++ {
		output := applyGlobalPrompt(input, prompt)
		if len(output) != len(input)+1 || output[0]["role"] != "system" || output[0]["content"] != prompt {
			t.Fatalf("round %d: global prompt missing or duplicated", round)
		}
		for i, original := range input {
			want := make(map[string]any, len(original))
			for key, value := range original {
				want[key] = value
			}
			if i < 2 {
				want["role"] = "user"
			}
			if !reflect.DeepEqual(output[i+1], want) {
				t.Fatalf("message %d changed content or ordering", i)
			}
		}
		output[1]["role"] = "changed"
		after, _ := json.Marshal(input)
		if string(before) != string(after) {
			t.Fatal("request messages mutated")
		}
	}
}

func TestGlobalPromptEmptyPreservesRoles(t *testing.T) {
	for _, prompt := range []string{"", " \r\n\t", "\u3000"} {
		input := []map[string]any{{"role": "system", "content": "original"}, {"role": "developer", "content": "instruction"}, {"role": "user", "content": "hello"}}
		if output := applyGlobalPrompt(input, prompt); !reflect.DeepEqual(output, input) {
			t.Fatalf("empty %q changed original messages", prompt)
		}
	}
}

func setIsolatedGlobalPrompt(t *testing.T, s *Server, prompt string) {
	t.Helper()
	var saved string
	if err := s.store.db.QueryRow(`SELECT global_prompt FROM ai_eder_service_config WHERE config_id=1`).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`UPDATE ai_eder_service_config SET global_prompt=? WHERE config_id=1`, prompt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.store.db.Exec(`UPDATE ai_eder_service_config SET global_prompt=? WHERE config_id=1`, saved); err != nil {
			t.Error(err)
		}
	})
}

func globalPromptChat(t *testing.T, s *Server, uid string, payload chatRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(raw))).WithContext(walletContext(uid, ""))
	w := httptest.NewRecorder()
	requestTrace(http.HandlerFunc(s.chat)).ServeHTTP(w, r)
	return w
}

// 使用实际数据库、预扣结算和模拟上游，验证每轮读取配置而非依赖客户端注入。
func TestGlobalPromptChatForwardingAndReload(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			var captured []map[string]any
			s, uid, model, calls := contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				captured = body.Messages
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
				}
			})
			setIsolatedGlobalPrompt(t, s, "全局规则第一版")
			payload := chatRequest{ClientInfo: isolatedClientInfo(), Model: model, MaxTokens: 8, Stream: stream, Messages: []map[string]any{{"role": "system", "content": "client instruction"}, {"role": "user", "content": "hello"}}}
			for _, prompt := range []string{"全局规则第一版", "第二版\n😀", ""} {
				if _, err := s.store.db.Exec(`UPDATE ai_eder_service_config SET global_prompt=? WHERE config_id=1`, prompt); err != nil {
					t.Fatal(err)
				}
				response := globalPromptChat(t, s, uid, payload)
				if response.Code != 200 {
					t.Fatalf("chat status=%d body=%s", response.Code, response.Body.String())
				}
				want := applyGlobalPrompt(payload.Messages, prompt)
				if !reflect.DeepEqual(captured, want) {
					t.Fatalf("unexpected upstream messages: %v", captured)
				}
				var persisted int64
				if err := s.store.db.QueryRow(`SELECT prompt_tokens FROM ai_usage WHERE request_id=?`, response.Header().Get("X-Request-ID")).Scan(&persisted); err != nil {
					t.Fatal(err)
				}
				serialized, _ := json.Marshal(map[string]any{"messages": captured, "tools": json.RawMessage(nil), "tool_choice": json.RawMessage(nil)})
				if wantEstimate := int64((len(serialized)+2)/3 + len(captured)*8); persisted != wantEstimate {
					t.Fatalf("global prompt not included in fallback billing: %d != %d", persisted, wantEstimate)
				}
			}
			if calls.Load() != 3 {
				t.Fatalf("upstream calls=%d", calls.Load())
			}
		})
	}
}

func TestGlobalPromptContextLimitBeforeBilling(t *testing.T) {
	s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("oversized global prompt reached upstream") })
	setIsolatedGlobalPrompt(t, s, strings.Repeat("规", 5000))
	if _, err := s.store.db.Exec(`UPDATE ai_model SET max_context_tokens=4096 WHERE model_id=?`, model); err != nil {
		t.Fatal(err)
	}
	response := globalPromptChat(t, s, uid, chatRequest{ClientInfo: isolatedClientInfo(), Model: model, MaxTokens: 1, Messages: []map[string]any{{"role": "user", "content": "hello"}}})
	if response.Code != 400 || !strings.Contains(response.Body.String(), "estimated prompt size exceeds") {
		t.Fatalf("status=%d: %s", response.Code, response.Body.String())
	}
	assertBalance(t, s, uid, 1000000000)
	var ledger int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_wallet_ledger WHERE uid=?`, uid).Scan(&ledger); err != nil || ledger != 0 {
		t.Fatalf("unexpected ledger rows=%d err=%v", ledger, err)
	}
	if calls.Load() != 0 {
		t.Fatal("rejected global prompt called upstream")
	}
}

func TestGlobalPromptConfigurationPrivacyAndLimits(t *testing.T) {
	s, _, _, _ := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("configuration called upstream") })
	setIsolatedGlobalPrompt(t, s, "private-global-instruction-😀")
	r := httptest.NewRequest(http.MethodGet, "/v1/service/config", nil)
	w := httptest.NewRecorder()
	s.ederServiceConfig(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "global_prompt") || strings.Contains(w.Body.String(), "private-global-instruction") {
		t.Fatalf("configuration leak or failure: %s", w.Body.String())
	}
	for _, size := range []int{10000, 10001} {
		if _, err := s.store.db.Exec(`UPDATE ai_eder_service_config SET global_prompt=? WHERE config_id=1`, strings.Repeat("😀", size)); err != nil {
			t.Fatal(err)
		}
		config, err := s.store.ederServiceConfig(context.Background())
		if size == 10000 && (err != nil || config.GlobalPrompt != strings.Repeat("😀", size)) {
			t.Fatalf("valid prompt rejected: %v", err)
		}
		if size == 10001 && err == nil {
			t.Fatal("oversized stored configuration accepted")
		}
	}
}
