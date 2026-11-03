package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func testImageURL(t *testing.T, format string, width, height int) string {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	picture.Set(0, 0, color.RGBA{R: 70, G: 120, B: 180, A: 255})
	var b bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&b, picture)
	} else {
		err = jpeg.Encode(&b, picture, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	mime := "image/" + format
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func testImageMessages(url string) []map[string]any {
	return []map[string]any{{"role": "user", "content": []any{map[string]any{"type": "text", "text": "describe this image"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url, "detail": "auto"}}}}}
}

func TestImageInputValidationAndEstimation(t *testing.T) {
	pngURL := testImageURL(t, "png", 32, 24)
	jpegURL := testImageURL(t, "jpeg", 48, 32)
	for _, url := range []string{pngURL, jpegURL} {
		input := testImageMessages(url)
		before, _ := json.Marshal(input)
		estimate, err := chatInputEstimate(input, nil, nil, true, modelImageInputPolicy("", ""))
		if err != nil || estimate < imageTokenEstimate || estimate > imageTokenEstimate+200 {
			t.Fatalf("unexpected image estimate=%d error=%v", estimate, err)
		}
		after, _ := json.Marshal(input)
		if !bytes.Equal(before, after) {
			t.Fatal("estimator altered upstream image or message")
		}
	}
	small, _ := chatInputEstimate(testImageMessages(pngURL), nil, nil, true, modelImageInputPolicy("", ""))
	largeURL := testImageURL(t, "png", 2048, 2048)
	large, err := chatInputEstimate(testImageMessages(largeURL), nil, nil, true, modelImageInputPolicy("", ""))
	if err != nil || large != small {
		t.Fatalf("image estimate depends on encoded text size: %d/%d %v", small, large, err)
	}
	text := []map[string]any{{"role": "system", "content": "rules"}, {"role": "user", "content": "hello"}, {"role": "assistant", "content": nil, "tool_calls": []any{}}, {"role": "tool", "content": "result", "tool_call_id": "test"}}
	raw, _ := json.Marshal(map[string]any{"messages": text, "tools": json.RawMessage(nil), "tool_choice": json.RawMessage(nil)})
	got, err := chatInputEstimate(text, nil, nil, false, modelImageInputPolicy("", ""))
	if err != nil || got != int64((len(raw)+2)/3+len(text)*8) {
		t.Fatal("text estimation contract changed")
	}
	invalid := map[string]string{
		"remote https":      "https://example.invalid/image.png",
		"remote private":    "http://127.0.0.1/secret",
		"svg":               "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>")),
		"bad base64":        "data:image/png;base64,not!base64",
		"empty":             "data:image/png;base64,",
		"invalid bytes":     "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not an image")),
		"mime spoof":        strings.Replace(pngURL, "image/png", "image/jpeg", 1),
		"oversized encoded": "data:image/png;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(4<<20)+4),
		"too wide":          testImageURL(t, "png", 2049, 1),
		"too tall":          testImageURL(t, "png", 1, 2049),
		"truncated":         "data:image/png;base64," + base64.StdEncoding.EncodeToString(mustImageBytes(t, pngURL)[:35]),
	}
	for name, url := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := chatInputEstimate(testImageMessages(url), nil, nil, true, modelImageInputPolicy("", "")); err == nil {
				t.Fatal("invalid image accepted")
			}
		})
	}
	t.Run("disabled", func(t *testing.T) {
		if _, err := chatInputEstimate(testImageMessages(pngURL), nil, nil, false, modelImageInputPolicy("", "")); err == nil {
			t.Fatal("image capability bypassed")
		}
	})
	t.Run("assistant image", func(t *testing.T) {
		m := testImageMessages(pngURL)
		m[0]["role"] = "assistant"
		if _, err := chatInputEstimate(m, nil, nil, true, modelImageInputPolicy("", "")); err == nil {
			t.Fatal("assistant image accepted")
		}
	})
	t.Run("invalid detail", func(t *testing.T) {
		m := testImageMessages(pngURL)
		m[0]["content"].([]any)[1].(map[string]any)["image_url"].(map[string]any)["detail"] = "secret"
		if _, err := chatInputEstimate(m, nil, nil, true, modelImageInputPolicy("", "")); err == nil {
			t.Fatal("invalid detail accepted")
		}
	})
	t.Run("history count", func(t *testing.T) {
		policy := modelImageInputPolicy("", "")
		policy.maxImages = 32
		m := []map[string]any{}
		for i := 0; i < 32; i++ {
			m = append(m, testImageMessages(pngURL)...)
		}
		if _, err := chatInputEstimate(m, nil, nil, true, policy); err != nil {
			t.Fatal(err)
		}
		m = append(m, testImageMessages(pngURL)...)
		if _, err := chatInputEstimate(m, nil, nil, true, policy); err == nil {
			t.Fatal("image history count not bounded")
		}
	})
	t.Run("history bytes", func(t *testing.T) {
		policy := modelImageInputPolicy("", "")
		policy.maxImages = 32
		data := append(mustImageBytes(t, pngURL), make([]byte, 3<<20)...)
		url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		m := append(testImageMessages(url), testImageMessages(url)...)
		if _, err := chatInputEstimate(m, nil, nil, true, policy); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 9; i++ {
			m = append(m, testImageMessages(url)...)
		}
		if _, err := chatInputEstimate(m, nil, nil, true, policy); err == nil {
			t.Fatal("decoded history size not bounded")
		}
	})
	for name, part := range map[string]any{"invalid part": "bad", "unknown type": map[string]any{"type": "input_audio"}, "missing image URL": map[string]any{"type": "image_url"}, "nontext URL": map[string]any{"type": "image_url", "image_url": map[string]any{"url": true}}} {
		t.Run(name, func(t *testing.T) {
			if _, err := chatInputEstimate([]map[string]any{{"role": "user", "content": []any{part}}}, nil, nil, true, modelImageInputPolicy("", "")); err == nil {
				t.Fatal("invalid content accepted")
			}
		})
	}
}

func mustImageBytes(t *testing.T, url string) []byte {
	t.Helper()
	parts := strings.SplitN(url, ",", 2)
	data, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestImageInputChatCapabilityAndForwarding(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			var forwarded []map[string]any
			s, uid, model, calls := contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				forwarded = body.Messages
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"seen\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9000,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"))
				} else {
					w.Write([]byte(`{"choices":[{"message":{"content":"seen"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9000,"completion_tokens":2}}`))
				}
			})
			input := testImageMessages(testImageURL(t, "png", 8, 8))
			payload := chatRequest{Model: model, Messages: input, ClientInfo: isolatedClientInfo(), Stream: stream, MaxTokens: 32}
			response := globalPromptChat(t, s, uid, payload)
			if response.Code != http.StatusBadRequest || calls.Load() != 0 {
				t.Fatalf("disabled model reached provider: status=%d calls=%d", response.Code, calls.Load())
			}
			var balance, ledger int64
			if err := s.store.db.QueryRow(`SELECT balance_micro FROM ai_wallet WHERE uid=?`, uid).Scan(&balance); err != nil || balance != 1000000000 {
				t.Fatalf("rejection changed balance: %d %v", balance, err)
			}
			if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_wallet_ledger WHERE uid=?`, uid).Scan(&ledger); err != nil || ledger != 0 {
				t.Fatal("rejection wrote financial ledger")
			}
			if _, err := s.store.db.Exec(`UPDATE ai_model SET allow_images=1 WHERE model_id=?`, model); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.db.Exec(`UPDATE ai_model_pricing SET vision_input_price_micro_per_1k=1000,vision_output_price_micro_per_1k=1000 WHERE model_id=?`, model); err != nil {
				t.Fatal(err)
			}
			response = globalPromptChat(t, s, uid, payload)
			if response.Code != http.StatusOK || calls.Load() != 1 {
				t.Fatalf("vision request failed: %d %s", response.Code, response.Body.String())
			}
			if !reflect.DeepEqual(forwarded, input) {
				t.Fatal("image content/order changed upstream")
			}
			var prompt int64
			if err := s.store.db.QueryRow(`SELECT prompt_tokens FROM ai_usage WHERE uid=? AND status='completed' ORDER BY usage_id DESC LIMIT 1`, uid).Scan(&prompt); err != nil || prompt != 9000 {
				t.Fatalf("authoritative image usage not settled: %d %v", prompt, err)
			}
			assertBalance(t, s, uid, 1000000000-9002)
		})
	}
}

func TestImageInputPublicCapabilities(t *testing.T) {
	s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("catalog unexpectedly called provider") })
	quotaAddLedger(t, s, uid, "admin_recharge", 1000000000)
	for _, enabled := range []bool{false, true} {
		if _, err := s.store.db.Exec(`UPDATE ai_model SET allow_images=? WHERE model_id=?`, enabled, model); err != nil {
			t.Fatal(err)
		}
		models, err := s.publicModelPrices(context.Background(), "17")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range models {
			if entry["id"] == model {
				found = true
				if entry["allow_images"] != enabled {
					t.Fatal("catalog image capability incorrect")
				}
			}
		}
		if !found {
			t.Fatal("model missing")
		}
		request := httptest.NewRequest(http.MethodGet, "/v1/account/quota", nil).WithContext(walletContext(uid, ""))
		response := httptest.NewRecorder()
		s.quota(response, request)
		var body struct {
			Models []struct {
				ID          string `json:"id"`
				AllowImages bool   `json:"allow_images"`
			} `json:"pricing_models"`
		}
		if err := json.Unmarshal(testSuccessData(t, response.Body.Bytes()), &body); err != nil {
			t.Fatal(err)
		}
		found = false
		for _, entry := range body.Models {
			if entry.ID == model {
				found = true
				if entry.AllowImages != enabled {
					t.Fatal("quota image capability incorrect")
				}
			}
		}
		if !found {
			t.Fatal("quota model missing")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("catalog made upstream calls")
	}
}
