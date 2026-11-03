package relay

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func morningCachePricing() modelPricing {
	return modelPricing{timeZone: "Asia/Shanghai", cacheInput: 100, peakEnabled: true,
		peakStart: sql.NullString{String: "09:00", Valid: true}, peakEnd: sql.NullString{String: "12:00", Valid: true},
		peakInput: 2000, peakCacheInput: 500, peakOutput: 8000, peakMultiplier: 10000,
		limitedInput: 500, limitedCacheInput: 50, limitedOutput: 2000, limitedMultiplier: 10000}
}

func TestMorningPeakSelectsIndependentCacheAndNoncacheRates(t *testing.T) {
	p := morningCachePricing()
	for _, tc := range []struct {
		at, kind                     string
		input, cache, output, charge int64
	}{
		{"2026-10-28T08:59:59+08:00", "standard", 1000, 100, 4000, 860},
		{"2026-10-28T09:00:00+08:00", "peak", 2000, 500, 8000, 1900},
		{"2026-10-28T11:59:59+08:00", "peak", 2000, 500, 8000, 1900},
		{"2026-10-28T12:00:00+08:00", "standard", 1000, 100, 4000, 860},
		{"2026-10-28T01:00:00Z", "peak", 2000, 500, 8000, 1900},
		{"2026-10-28T04:00:00Z", "standard", 1000, 100, 4000, 860},
	} {
		t.Run(tc.at, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			r, err := p.resolve(at, 1000, 4000, 10000)
			if err != nil || r.kind != tc.kind || r.input != tc.input || r.cacheInput != tc.cache || r.output != tc.output || !r.lockedAt.Equal(at) {
				t.Fatalf("rate=%+v err=%v", r, err)
			}
			charge, err := priceWithCache(1000, 600, 100, r.input, r.cacheInput, r.output, r.multiplier)
			if err != nil || charge != tc.charge {
				t.Fatalf("charge=%d want=%d err=%v", charge, tc.charge, err)
			}
		})
	}
	// 同一配置按模型时区解释，纽约夏令时与冬令时对应不同 UTC 起点。
	p.timeZone = "America/New_York"
	for _, stamp := range []string{"2026-07-01T13:00:00Z", "2026-12-01T14:00:00Z"} {
		at, _ := time.Parse(time.RFC3339, stamp)
		r, err := p.resolve(at, 1000, 4000, 10000)
		if err != nil || r.kind != "peak" || r.cacheInput != 500 {
			t.Fatalf("DST cache rate=%+v err=%v", r, err)
		}
	}
}

func TestScheduledCacheSnapshotPersistsAcrossPolicyChanges(t *testing.T) {
	for _, tc := range []struct {
		name, stamp, kind string
		charge            int64
	}{
		{"low", "2026-10-28T08:59:59+08:00", "standard", 860},
		{"peak", "2026-10-28T09:00:00+08:00", "peak", 1900},
		{"limited overrides peak", "2026-10-28T10:00:00+08:00", "limited", 430},
		{"limited end restores peak", "2026-10-28T11:00:00+08:00", "peak", 1900},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := walletServer(t)
			uid := addWallet(t, s, 1000000)
			p := morningCachePricing()
			start, _ := time.Parse(time.RFC3339, "2026-10-28T10:00:00+08:00")
			p.limitedEnabled = true
			p.limitedStart, p.limitedEnd = sql.NullInt64{Int64: start.Unix(), Valid: true}, sql.NullInt64{Int64: start.Add(time.Hour).Unix(), Valid: true}
			at, _ := time.Parse(time.RFC3339, tc.stamp)
			rate, err := p.resolve(at, 1000, 4000, 10000)
			if err != nil {
				t.Fatal(err)
			}
			if rate.kind != tc.kind {
				t.Fatalf("kind=%s want=%s", rate.kind, tc.kind)
			}
			ctx, request := walletContext(uid, ""), newRequestID()
			if err = s.reserve(ctx, uid, request, "scheduled-cache", 10000, 0, 0, 1100); err != nil {
				t.Fatal(err)
			}
			assertBalance(t, s, uid, 990000)
			// 后续配置或时段变化不应改写已锁定的三种单价。
			p.cacheInput, p.peakInput, p.peakCacheInput, p.limitedCacheInput = 999999, 999999, 999999, 999999
			rate.cachedTokens = 600
			actual, err := priceWithCache(1000, 600, 100, rate.input, rate.cacheInput, rate.output, rate.multiplier)
			if err != nil || actual != tc.charge {
				t.Fatalf("charge=%d want=%d err=%v", actual, tc.charge, err)
			}
			if err = s.settle(ctx, uid, request, "scheduled-cache", 1000, 100, 10000, actual, rate); err != nil {
				t.Fatal(err)
			}
			assertBalance(t, s, uid, 1000000-tc.charge)
			var kind string
			var input, cache, output, cached, charged int64
			if err = s.store.db.QueryRow(`SELECT p.price_kind,p.input_price_micro_per_1k,p.cache_input_price_micro_per_1k,p.output_price_micro_per_1k,u.cached_prompt_tokens,u.charged_micro FROM ai_usage_pricing p JOIN ai_usage u ON u.request_id=p.request_id WHERE p.request_id=?`, request).Scan(&kind, &input, &cache, &output, &cached, &charged); err != nil {
				t.Fatal(err)
			}
			if kind != rate.kind || input != rate.input || cache != rate.cacheInput || output != rate.output || cached != 600 || charged != actual {
				t.Fatalf("persisted snapshot=%s/%d/%d/%d cached=%d charged=%d", kind, input, cache, output, cached, charged)
			}
		})
	}
}

func TestChatReservesHigherCacheRateAndSettlesActualCacheUsage(t *testing.T) {
	var observed atomic.Bool
	var s *Server
	var uid string
	s, uid, model, _ := contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
		var reserved int64
		if err := s.store.db.QueryRow(`SELECT -delta_micro FROM ai_wallet_ledger WHERE uid=? AND reason='reserve' ORDER BY ledger_id DESC LIMIT 1`, uid).Scan(&reserved); err != nil {
			t.Error(err)
		}
		if reserved < 100 {
			t.Errorf("cache pricing not reserved: %d", reserved)
		}
		observed.Store(true)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"prompt_cache_hit_tokens":1,"completion_tokens":0}}`)
	})
	if _, err := s.store.db.Exec(`UPDATE ai_model SET input_price_micro_per_1k=0,cache_input_price_micro_per_1k=100000,output_price_micro_per_1k=0 WHERE model_id=?`, model); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(chatRequest{ClientInfo: isolatedClientInfo(), Model: model, Messages: []map[string]any{{"role": "user", "content": "hello"}}, MaxTokens: 32})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(payload))).WithContext(walletContext(uid, ""))
	request.Header.Set("Idempotency-Key", newRequestID())
	response := httptest.NewRecorder()
	s.chat(response, request)
	if response.Code != 200 || !observed.Load() {
		t.Fatalf("status=%d observed=%v response=%s", response.Code, observed.Load(), response.Body.String())
	}
	assertBalance(t, s, uid, 999999900)
}
