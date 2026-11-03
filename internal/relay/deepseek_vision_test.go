package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestDeepSeekVisionPolicyIdentity(t *testing.T) {
	for _, base := range []string{"https://api.deepseek.com", "https://API.DEEPSEEK.COM/", "https://api.deepseek.com:443/v1/", "https://api.deepseek.com/chat/completions", "https://api.deepseek.com/v1/chat/completions/", "https://api.deepseek.com/%76%31"} {
		for _, model := range []string{"deepseek-flash", "deepseek-v4-flash-vision-exp"} {
			policy := modelImageInputPolicy(base, model)
			if policy.tokensPerImage != 1024 || !policy.allowOriginal {
				t.Fatalf("official policy rejected %s/%s", base, model)
			}
		}
	}
	for _, tc := range []struct{ base, model string }{
		{"http://api.deepseek.com", "deepseek-flash"}, {"https://api.deepseek.com:444", "deepseek-flash"},
		{"https://api.deepseek.com.evil.invalid", "deepseek-flash"}, {"https://evil.invalid/api.deepseek.com", "deepseek-flash"},
		{"https://api.deepseek.com@evil.invalid", "deepseek-flash"}, {"https://user@api.deepseek.com", "deepseek-flash"},
		{"https://api.deepseek.com?route=proxy", "deepseek-flash"}, {"https://api.deepseek.com#x", "deepseek-flash"},
		{"https://api.deepseek.com/other", "deepseek-flash"},
		{"https://api.deepseek.com", "deepseek-chat"}, {"https://api.deepseek.com", "deepseek-reasoner"},
		{"https://api.deepseek.com", "deepseek-flash-other"}, {"https://api.deepseek.com", "DEEPSEEK-FLASH"},
		{"https://api.deepseek.com", "deepseek-flash "}, {"https://third-party.invalid/v1", "deepseek-flash"},
		{"https://%", "deepseek-flash"}, {"", ""},
	} {
		policy := modelImageInputPolicy(tc.base, tc.model)
		if policy.tokensPerImage != imageTokenEstimate || policy.allowOriginal {
			t.Fatalf("unverified route/model gained official policy: %s/%s", tc.base, tc.model)
		}
	}
}

func TestDeepSeekVisionBudgetAndDetail(t *testing.T) {
	image := testImageURL(t, "png", 8, 8)
	for _, count := range []int{1, 2, 4} {
		messages := testImageMessages(image)
		for i := 1; i < count; i++ {
			messages[0]["content"] = append(messages[0]["content"].([]any), map[string]any{"type": "image_url", "image_url": map[string]any{"url": image, "detail": "original"}})
		}
		messages[0]["content"].([]any)[1].(map[string]any)["image_url"].(map[string]any)["detail"] = "original"
		messages = append(messages, map[string]any{"role": "assistant", "content": "description"}, map[string]any{"role": "user", "content": "follow up"})
		before, _ := json.Marshal(messages)
		deep, err := chatInputEstimate(messages, nil, nil, true, modelImageInputPolicy("https://api.deepseek.com", "deepseek-flash"))
		if err != nil {
			t.Fatal(err)
		}
		after, _ := json.Marshal(messages)
		if string(after) != string(before) {
			t.Fatal("estimator mutated image messages")
		}
		for _, m := range messages {
			if parts, ok := m["content"].([]any); ok {
				for _, raw := range parts {
					part := raw.(map[string]any)
					if part["type"] == "image_url" {
						part["image_url"].(map[string]any)["detail"] = "auto"
					}
				}
			}
		}
		generic, err := chatInputEstimate(messages, nil, nil, true, modelImageInputPolicy("https://other.invalid", "deepseek-flash"))
		if err != nil {
			t.Fatal(err)
		}
		if generic-deep != int64(count)*(8192-1024) {
			t.Fatalf("count=%d generic=%d official=%d", count, generic, deep)
		}
		// original只影响detail，不应改写请求对象。
		var restored []map[string]any
		json.Unmarshal(before, &restored)
		for _, policy := range []imageInputPolicy{{}, {tokensPerImage: -1}} {
			if _, err := chatInputEstimate(restored, nil, nil, true, policy); err == nil {
				t.Fatal("invalid budget accepted")
			}
		}
		if _, err := chatInputEstimate(restored, nil, nil, true, modelImageInputPolicy("", "")); err == nil {
			t.Fatal("generic model accepted original")
		}
		if _, err := chatInputEstimate(restored, nil, nil, false, modelImageInputPolicy("https://api.deepseek.com", "deepseek-flash")); err == nil {
			t.Fatal("official policy bypassed disabled capability")
		}
		text := []map[string]any{{"role": "user", "content": "text only"}}
		a, _ := chatInputEstimate(text, nil, nil, false, modelImageInputPolicy("", ""))
		b, _ := chatInputEstimate(text, nil, nil, false, modelImageInputPolicy("https://api.deepseek.com", "deepseek-flash"))
		if a != b {
			t.Fatal("text estimate changed")
		}
	}
}

type deepSeekLocalTransport struct {
	inner  http.RoundTripper
	target *url.URL
	t      *testing.T
}

func (d deepSeekLocalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.deepseek.com" || r.URL.Path != "/chat/completions" {
		d.t.Errorf("unexpected upstream route %s", r.URL.String())
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = d.target.Scheme
	u.Host = d.target.Host
	copy.URL = &u
	copy.Host = ""
	return d.inner.RoundTrip(copy)
}

// 官方域名仅参与策略选择，实际HTTP始终重定向到本地模拟服务器，不连接真实供应商。
func TestDeepSeekVisionRealReservationAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name, period, upstream string
		stream, usage          bool
	}{
		{"json_usage", "standard", "deepseek-flash", false, true},
		{"stream_usage", "peak", "deepseek-flash", true, true},
		{"legacy_missing_usage", "limited", "deepseek-v4-flash-vision-exp", false, false},
		{"stream_missing_usage", "standard", "deepseek-flash", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s *Server
			var uid, model string
			var reserve int64
			var id string
			var forwarded []map[string]any
			s, uid, model, _ = contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string           `json:"model"`
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				forwarded = body.Messages
				if body.Model != tc.upstream {
					t.Error("upstream model changed")
				}
				if err := s.store.db.QueryRow(`SELECT request_id,-delta_micro FROM ai_wallet_ledger WHERE uid=? AND reason='reserve' ORDER BY ledger_id DESC LIMIT 1`, uid).Scan(&id, &reserve); err != nil {
					t.Error(err)
				}
				if _, err := s.store.db.Exec(`UPDATE ai_model_pricing SET vision_input_price_micro_per_1k=999999,peak_vision_input_price_micro_per_1k=999999,limited_vision_input_price_micro_per_1k=999999 WHERE model_id=?`, model); err != nil {
					t.Error(err)
				}
				frame := `{"choices":[{"message":{"content":"ok"},"delta":{"content":"ok"},"finish_reason":"stop"}]`
				if tc.usage {
					frame += `,"usage":{"prompt_tokens":1000,"prompt_cache_hit_tokens":100,"completion_tokens":16}`
				}
				frame += "}"
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
				} else {
					fmt.Fprint(w, frame)
				}
			})
			var localURL string
			if err := s.store.db.QueryRow(`SELECT p.base_url FROM ai_model m JOIN ai_provider p ON p.provider_id=m.provider_id WHERE m.model_id=?`, model).Scan(&localURL); err != nil {
				t.Fatal(err)
			}
			target, err := url.Parse(localURL)
			if err != nil {
				t.Fatal(err)
			}
			s.client.Transport = deepSeekLocalTransport{inner: s.client.Transport, target: target, t: t}
			if _, err := s.store.db.Exec(`UPDATE ai_provider p JOIN ai_model m ON m.provider_id=p.provider_id SET p.base_url='https://api.deepseek.com',m.upstream_model=?,m.allow_images=1 WHERE m.model_id=?`, tc.upstream, model); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			start, end := now.Add(-5*time.Minute).Format("15:04"), now.Add(5*time.Minute).Format("15:04")
			if _, err := s.store.db.Exec(`UPDATE ai_model_pricing SET time_zone='UTC',peak_enabled=?,peak_start=?,peak_end=?,limited_enabled=?,limited_start_unix=?,limited_end_unix=?,vision_input_price_micro_per_1k=2000,vision_cache_input_price_micro_per_1k=3000,vision_output_price_micro_per_1k=4000,peak_vision_input_price_micro_per_1k=5000,peak_vision_cache_input_price_micro_per_1k=6000,peak_vision_output_price_micro_per_1k=7000,limited_vision_input_price_micro_per_1k=8000,limited_vision_cache_input_price_micro_per_1k=9000,limited_vision_output_price_micro_per_1k=10000 WHERE model_id=?`, tc.period == "peak", start, end, tc.period == "limited", now.Unix()-300, now.Unix()+300, model); err != nil {
				t.Fatal(err)
			}
			messages := testImageMessages(testImageURL(t, "png", 8, 8))
			messages[0]["content"].([]any)[1].(map[string]any)["image_url"].(map[string]any)["detail"] = "original"
			messages = append(messages, map[string]any{"role": "assistant", "content": "prior"}, map[string]any{"role": "user", "content": "follow up"})
			response := globalPromptChat(t, s, uid, chatRequest{Model: model, Messages: messages, ClientInfo: isolatedClientInfo(), Stream: tc.stream, MaxTokens: 32})
			if response.Code != 200 {
				t.Fatalf("status=%d %s", response.Code, response.Body.String())
			}
			if !reflect.DeepEqual(forwarded, messages) {
				t.Fatal("image message changed")
			}
			in, cache, out := int64(2000), int64(3000), int64(4000)
			if tc.period == "peak" {
				in, cache, out = 5000, 6000, 7000
			}
			if tc.period == "limited" {
				in, cache, out = 8000, 9000, 10000
			}
			estimate, err := chatInputEstimate(messages, nil, nil, true, modelImageInputPolicy("https://api.deepseek.com", tc.upstream))
			if err != nil {
				t.Fatal(err)
			}
			wantReserve, err := price(estimate, 32, cache, out, 10000)
			if err != nil || reserve != wantReserve {
				t.Fatalf("reserve=%d expected=%d %v", reserve, wantReserve, err)
			}
			prompt, cached := estimate, int64(0)
			if tc.usage {
				prompt, cached = 1000, 100
			}
			actual, err := priceWithCache(prompt, cached, 16, in, cache, out, 10000)
			if err != nil {
				t.Fatal(err)
			}
			var charged, promptGot int64
			var kind, family string
			if err := s.store.db.QueryRow(`SELECT u.charged_micro,u.prompt_tokens,p.price_kind,p.billing_input_kind FROM ai_usage u JOIN ai_usage_pricing p ON p.request_id=u.request_id WHERE u.request_id=?`, id).Scan(&charged, &promptGot, &kind, &family); err != nil {
				t.Fatal(err)
			}
			if charged != actual || promptGot != prompt || kind != tc.period || family != "vision" {
				t.Fatalf("snapshot charge=%d/%d prompt=%d/%d kind=%s family=%s", charged, actual, promptGot, prompt, kind, family)
			}
			assertBalance(t, s, uid, 1000000000-actual)
		})
	}
}
