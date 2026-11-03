package relay

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func visionPricingPolicy() modelPricing {
	return modelPricing{timeZone: "Asia/Shanghai", cacheInput: 200, peakEnabled: true, peakStart: sql.NullString{String: "09:00", Valid: true}, peakEnd: sql.NullString{String: "12:00", Valid: true}, peakInput: 400, peakCacheInput: 500, peakOutput: 600, peakMultiplier: 10000, limitedEnabled: true, limitedStart: sql.NullInt64{Int64: time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC).Unix(), Valid: true}, limitedEnd: sql.NullInt64{Int64: time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC).Unix(), Valid: true}, limitedInput: 700, limitedCacheInput: 800, limitedOutput: 900, limitedMultiplier: 10000, visionInput: 2000, visionCacheInput: 3000, visionOutput: 4000, peakVisionInput: 5000, peakVisionCacheInput: 6000, peakVisionOutput: 7000, limitedVisionInput: 8000, limitedVisionCacheInput: 9000, limitedVisionOutput: 10000}
}

func TestVisionPricingFamiliesAndPeriodBoundaries(t *testing.T) {
	p := visionPricingPolicy()
	for _, tc := range []struct {
		name, clock, kind string
		text, vision      int64
	}{{"before_peak", "00:59", "standard", 100, 2000}, {"peak_start", "01:00", "peak", 400, 5000}, {"limited_start", "02:00", "limited", 700, 8000}, {"limited_end", "03:00", "peak", 400, 5000}, {"peak_end", "04:00", "standard", 100, 2000}} {
		t.Run(tc.name, func(t *testing.T) {
			now, err := time.Parse("2006-01-02 15:04", "2026-10-30 "+tc.clock)
			if err != nil {
				t.Fatal(err)
			}
			for _, vision := range []bool{false, true} {
				rate, err := p.resolveForInput(now, 100, 300, 10000, vision)
				want := tc.text
				if vision {
					want = tc.vision
				}
				if err != nil || rate.kind != tc.kind || rate.input != want || rate.vision != vision || rate.inputKind() != map[bool]string{false: "text", true: "vision"}[vision] {
					t.Fatalf("family=%v rate=%+v err=%v", vision, rate, err)
				}
			}
		})
	}
	p.limitedEnabled = false
	p.peakStart.String = "23:00"
	p.peakEnd.String = "07:00"
	for _, tc := range []struct {
		hour int
		kind string
	}{{14, "standard"}, {15, "peak"}, {22, "peak"}, {23, "standard"}} {
		now := time.Date(2026, 10, 30, tc.hour, 0, 0, 0, time.UTC)
		rate, err := p.resolveForInput(now, 100, 300, 10000, true)
		if err != nil || rate.kind != tc.kind {
			t.Fatalf("cross midnight %d: %+v %v", tc.hour, rate, err)
		}
	}
	p.visionInput = -1
	if _, err := p.resolveForInput(time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), 100, 300, 10000, true); err == nil {
		t.Fatal("invalid vision price accepted")
	}
	if _, err := p.resolveForInput(time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), 100, 300, 10000, false); err != nil {
		t.Fatal("vision pricing leaked into text family")
	}
}

// 含历史图片的追问/工具续轮仍选同一价格族，文字和图片token合计只收一次。
func TestVisionPricingChatSettlementAndLockedSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		stream, history      bool
		period               string
		input, cache, output int64
	}{{"standard_json", false, false, "standard", 2000, 3000, 4000}, {"peak_stream", true, false, "peak", 5000, 6000, 7000}, {"activity_history", false, true, "limited", 8000, 9000, 10000}} {
		t.Run(tc.name, func(t *testing.T) {
			var s *Server
			var uid, model string
			var forwarded []map[string]any
			var observedReserve int64
			var requestID string
			s, uid, model, _ = contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				forwarded = body.Messages
				if err := s.store.db.QueryRow(`SELECT request_id,-delta_micro FROM ai_wallet_ledger WHERE uid=? AND reason='reserve' ORDER BY ledger_id DESC LIMIT 1`, uid).Scan(&requestID, &observedReserve); err != nil {
					t.Error(err)
				}
				// 模拟排队或供应商执行期间后台改价，已锁定价格必须不变。
				if _, err := s.store.db.Exec(`UPDATE ai_model_pricing SET vision_input_price_micro_per_1k=999999,peak_vision_input_price_micro_per_1k=999999,limited_vision_input_price_micro_per_1k=999999 WHERE model_id=?`, model); err != nil {
					t.Error(err)
				}
				frame := `{"choices":[{"message":{"content":"ok"},"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"prompt_cache_hit_tokens":400,"completion_tokens":100}}`
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
				} else {
					fmt.Fprint(w, frame)
				}
			})
			if _, err := s.store.db.Exec(`UPDATE ai_model SET allow_images=1,input_price_micro_per_1k=100,cache_input_price_micro_per_1k=200,output_price_micro_per_1k=300 WHERE model_id=?`, model); err != nil {
				t.Fatal(err)
			}
			peak, limited := tc.period == "peak", tc.period == "limited"
			// 高峰区间覆盖当前分钟，但保留同一模型时区解释。
			now := time.Now().UTC()
			start := now.Add(-5 * time.Minute).Format("15:04")
			end := now.Add(5 * time.Minute).Format("15:04")
			if _, err := s.store.db.Exec(`UPDATE ai_model_pricing SET time_zone='UTC',peak_enabled=?,peak_start=?,peak_end=?,limited_enabled=?,limited_start_unix=?,limited_end_unix=?,peak_input_price_micro_per_1k=400,peak_cache_input_price_micro_per_1k=500,peak_output_price_micro_per_1k=600,limited_input_price_micro_per_1k=700,limited_cache_input_price_micro_per_1k=800,limited_output_price_micro_per_1k=900,vision_input_price_micro_per_1k=2000,vision_cache_input_price_micro_per_1k=3000,vision_output_price_micro_per_1k=4000,peak_vision_input_price_micro_per_1k=5000,peak_vision_cache_input_price_micro_per_1k=6000,peak_vision_output_price_micro_per_1k=7000,limited_vision_input_price_micro_per_1k=8000,limited_vision_cache_input_price_micro_per_1k=9000,limited_vision_output_price_micro_per_1k=10000 WHERE model_id=?`, peak, start, end, limited, now.Unix()-60, now.Unix()+60, model); err != nil {
				t.Fatal(err)
			}
			messages := testImageMessages(testImageURL(t, "png", 8, 8))
			if tc.history {
				messages = append(messages, map[string]any{"role": "assistant", "content": "prior image description", "tool_calls": []any{map[string]any{"id": "call-v", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}}, map[string]any{"role": "tool", "tool_call_id": "call-v", "content": "done"}, map[string]any{"role": "user", "content": "follow up on that image"})
			}
			payload := chatRequest{Model: model, Messages: messages, ClientInfo: isolatedClientInfo(), Stream: tc.stream, MaxTokens: 256}
			response := globalPromptChat(t, s, uid, payload)
			if response.Code != 200 {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if !reflect.DeepEqual(forwarded, messages) {
				t.Fatal("forwarded mixed/history input changed")
			}
			estimate, err := chatInputEstimate(messages, nil, nil, true, modelImageInputPolicy("", ""))
			if err != nil {
				t.Fatal(err)
			}
			wantReserve, err := price(estimate, 256, tc.cache, tc.output, 10000)
			if err != nil || observedReserve != wantReserve {
				t.Fatalf("reserve=%d want=%d err=%v", observedReserve, wantReserve, err)
			}
			actual, err := priceWithCache(1000, 400, 100, tc.input, tc.cache, tc.output, 10000)
			if err != nil {
				t.Fatal(err)
			}
			var kind, family string
			var input, cache, output, charge, cached int64
			if err := s.store.db.QueryRow(`SELECT p.price_kind,p.billing_input_kind,p.input_price_micro_per_1k,p.cache_input_price_micro_per_1k,p.output_price_micro_per_1k,u.charged_micro,u.cached_prompt_tokens FROM ai_usage_pricing p JOIN ai_usage u ON u.request_id=p.request_id WHERE p.request_id=?`, requestID).Scan(&kind, &family, &input, &cache, &output, &charge, &cached); err != nil {
				t.Fatal(err)
			}
			if kind != tc.period || family != "vision" || input != tc.input || cache != tc.cache || output != tc.output || charge != actual || cached != 400 {
				t.Fatalf("snapshot=%s/%s/%d/%d/%d charge=%d expected=%d", kind, family, input, cache, output, charge, actual)
			}
			assertBalance(t, s, uid, 1000000000-actual)
			if err := s.settle(context.Background(), uid, requestID, model, 1000, 100, observedReserve, actual, billingRate{kind: tc.period, vision: true, timeZone: "UTC", lockedAt: now, input: tc.input, cacheInput: tc.cache, output: tc.output, multiplier: 10000}); err == nil {
				t.Fatal("duplicate settlement accepted")
			}
			assertBalance(t, s, uid, 1000000000-actual)
		})
	}
}

func TestVisionPricingFailureRefundAndCatalog(t *testing.T) {
	s, uid, model, calls := contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "provider failed", 502) })
	if _, err := s.store.db.Exec(`UPDATE ai_model SET allow_images=1 WHERE model_id=?`, model); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`UPDATE ai_model_pricing SET vision_input_price_micro_per_1k=2000,vision_cache_input_price_micro_per_1k=3000,vision_output_price_micro_per_1k=4000 WHERE model_id=?`, model); err != nil {
		t.Fatal(err)
	}
	response := globalPromptChat(t, s, uid, chatRequest{Model: model, Messages: testImageMessages(testImageURL(t, "jpeg", 8, 8)), ClientInfo: isolatedClientInfo(), MaxTokens: 32})
	if response.Code != 502 || calls.Load() != 1 {
		t.Fatalf("failure response=%d calls=%d", response.Code, calls.Load())
	}
	assertBalance(t, s, uid, 1000000000)
	var count int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_usage_pricing WHERE uid=?`, uid).Scan(&count); err != nil || count != 0 {
		t.Fatal("failure left pricing snapshot")
	}
	quotaAddLedger(t, s, uid, "admin_recharge", 1000000000)
	for _, endpoint := range []string{"models", "quota"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/"+endpoint, nil).WithContext(walletContext(uid, ""))
		w := httptest.NewRecorder()
		if endpoint == "models" {
			s.models(w, request)
		} else {
			s.quota(w, request)
		}
		raw := testSuccessData(t, w.Body.Bytes())
		if strings.Contains(string(raw), "cost_micro") || strings.Contains(string(raw), "secret_value") {
			t.Fatal("private pricing leaked")
		}
		var list []map[string]any
		if endpoint == "models" {
			var data struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			list = data.Data
		} else {
			var data struct {
				Models []map[string]any `json:"pricing_models"`
			}
			if err := json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			list = data.Models
		}
		found := false
		for _, entry := range list {
			if entry["id"] == model {
				found = true
				v, ok := entry["vision_pricing"].(map[string]any)
				if !ok || v["input_credit_per_1k"] != "0.002" || v["cache_input_credit_per_1k"] != "0.003" || v["output_credit_per_1k"] != "0.004" {
					t.Fatalf("public vision pricing=%v", entry)
				}
			}
		}
		if !found {
			t.Fatal("model missing")
		}
	}
}

func TestVisionPricingSnapshotConflictRollsBack(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, 100000)
	ctx := walletContext(uid, "")
	id := newRequestID()
	rate := billingRate{vision: true, kind: "standard", timeZone: "UTC", lockedAt: time.Now(), input: 2000, cacheInput: 3000, output: 4000, multiplier: 10000}
	if err := s.reserve(ctx, uid, id, "vision-snapshot-test", 10000, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`INSERT INTO ai_usage_pricing(request_id,uid,model_id,price_kind,billing_input_kind,time_zone,price_locked_at,input_price_micro_per_1k,cache_input_price_micro_per_1k,output_price_micro_per_1k,multiplier_bps) VALUES(?,?,?,'standard','vision','UTC',NOW(),2000,3000,4000,10000)`, id, uid, "vision-snapshot-test"); err != nil {
		t.Fatal(err)
	}
	if err := s.settle(ctx, uid, id, "vision-snapshot-test", 1000, 100, 10000, 2800, rate); err == nil {
		t.Fatal("snapshot collision accepted")
	}
	assertBalance(t, s, uid, 90000)
	var n int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_usage WHERE request_id=?`, id).Scan(&n); err != nil || n != 0 {
		t.Fatal("partial usage committed")
	}
	if err := s.refund(ctx, uid, id, 10000); err != nil {
		t.Fatal(err)
	}
	assertBalance(t, s, uid, 100000)
	if _, err := s.store.db.Exec(`DELETE FROM ai_rate_window WHERE model_id='vision-snapshot-test'`); err != nil {
		t.Error(err)
	}
}
