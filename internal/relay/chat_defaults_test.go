package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestEderDefaultReasoningCapabilityBounds(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		allowed      []string
		want         string
		bad          bool
	}{
		{"exact", "medium", []string{"none", "medium", "high"}, "medium", false},
		{"above maximum", "max", []string{"none", "low", "high"}, "high", false},
		{"missing intermediate", "high", []string{"none", "low", "max"}, "low", false},
		{"unordered maximum", "max", []string{"high", "none", "low"}, "high", false},
		{"unordered minimum", "none", []string{"high", "medium", "low"}, "low", false},
		{"thinking disabled", "max", []string{}, "none", false},
		{"no thinking policy", "high", nil, "none", false},
		{"only none", "max", []string{"none"}, "none", false},
		{"only high", "low", []string{"high"}, "high", false},
		{"minimal exact", "minimal", []string{"none", "minimal", "max"}, "minimal", false},
		{"invalid requested", "turbo", []string{"none"}, "", true},
		{"empty requested", "", []string{"none"}, "", true},
		{"invalid requested with no thinking", "turbo", nil, "", true},
		{"invalid capability", "max", []string{"none", "turbo"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := defaultReasoningEffort(tc.target, tc.allowed)
			if (err != nil) != tc.bad || !tc.bad && got != tc.want {
				t.Fatalf("default=%q err=%v want=%q", got, err, tc.want)
			}
		})
	}
	for _, effort := range reasoningTiers {
		t.Run("all ranks/"+effort, func(t *testing.T) {
			got, err := defaultReasoningEffort(effort, reasoningTiers[:])
			if err != nil || got != effort {
				t.Fatalf("rank not preserved: %s %v", got, err)
			}
		})
	}
}

func setIsolatedChatDefaults(t *testing.T, s *Server, effort string, tokens int) {
	t.Helper()
	var savedEffort string
	var savedTokens int
	if err := s.store.db.QueryRow(`SELECT default_reasoning_effort,default_context_tokens FROM ai_eder_service_config WHERE config_id=1`).Scan(&savedEffort, &savedTokens); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`UPDATE ai_eder_service_config SET default_reasoning_effort=?,default_context_tokens=? WHERE config_id=1`, effort, tokens); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.store.db.Exec(`UPDATE ai_eder_service_config SET default_reasoning_effort=?,default_context_tokens=? WHERE config_id=1`, savedEffort, savedTokens); err != nil {
			t.Error(err)
		}
	})
}

func defaultCatalogItem(t *testing.T, s *Server, uid, model string, quota bool) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil).WithContext(walletContext(uid, ""))
	w := httptest.NewRecorder()
	if quota {
		s.quota(w, r)
	} else {
		s.models(w, r)
	}
	if w.Code != 200 {
		t.Fatalf("catalog=%d: %s", w.Code, w.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(testSuccessData(t, w.Body.Bytes()), &data); err != nil {
		t.Fatal(err)
	}
	key := "data"
	if quota {
		key = "pricing_models"
	}
	for _, raw := range data[key].([]any) {
		item := raw.(map[string]any)
		if item["id"] == model {
			return item
		}
	}
	t.Fatal("model omitted from catalog")
	return nil
}

func TestEderDefaultsCatalogAndQuotaReloadWithinModelBounds(t *testing.T) {
	s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("catalog must not call provider") })
	setIsolatedChatDefaults(t, s, "max", 65536)
	quotaAddLedger(t, s, uid, "admin_recharge", 1000000000)
	for _, tc := range []struct {
		name, effort, allowed, wantEffort string
		target, max, wantContext          int
		thinking                          int
	}{
		{"64K below 128K", "max", `["none","low","high"]`, "high", 65536, 131072, 65536, 1},
		{"64K above 32K", "max", `["none","low","high"]`, "high", 65536, 32768, 32768, 1},
		{"lower target", "medium", `["none","low","high"]`, "low", 16384, 131072, 16384, 1},
		{"disabled thinking", "max", `["none","low","high"]`, "none", 1048576, 65536, 65536, 0},
		{"unsorted capability", "high", `["max","low","none"]`, "low", 4096, 32768, 4096, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.store.db.Exec(`UPDATE ai_eder_service_config SET default_reasoning_effort=?,default_context_tokens=? WHERE config_id=1`, tc.effort, tc.target); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.db.Exec(`UPDATE ai_model SET max_context_tokens=?,allow_thinking=?,reasoning_efforts=? WHERE model_id=?`, tc.max, tc.thinking, tc.allowed, model); err != nil {
				t.Fatal(err)
			}
			for _, quota := range []bool{false, true} {
				item := defaultCatalogItem(t, s, uid, model, quota)
				if item["default_reasoning_effort"] != tc.wantEffort || item["default_context_tokens"] != float64(tc.wantContext) {
					t.Fatalf("quota=%t effort=%v context=%v", quota, item["default_reasoning_effort"], item["default_context_tokens"])
				}
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("configuration query called provider")
	}
	var balance int64
	if err := s.store.db.QueryRow(`SELECT balance_micro FROM ai_wallet WHERE uid=?`, uid).Scan(&balance); err != nil || balance != 1000000000 {
		t.Fatalf("catalog mutated wallet: %d %v", balance, err)
	}
}

func TestEderDefaultsConfigPrivacyAndInvalidContext(t *testing.T) {
	s, _, _, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("configuration called provider") })
	setIsolatedChatDefaults(t, s, "xhigh", 262144)
	setIsolatedGlobalPrompt(t, s, "private-defaults-rule")
	w := httptest.NewRecorder()
	s.ederServiceConfig(w, httptest.NewRequest(http.MethodGet, "/v1/service/config", nil))
	var data map[string]any
	if err := json.Unmarshal(testSuccessData(t, w.Body.Bytes()), &data); err != nil {
		t.Fatal(err)
	}
	if data["default_reasoning_effort"] != "xhigh" || data["default_context_tokens"] != float64(262144) || strings.Contains(w.Body.String(), "global_prompt") || strings.Contains(w.Body.String(), "private-defaults-rule") {
		t.Fatalf("wrong defaults or private data leaked: %s", w.Body.String())
	}
	for _, bad := range []int{0, 6000, 1000000, 1048577} {
		t.Run("invalid context "+strconv.Itoa(bad), func(t *testing.T) {
			if _, err := s.store.db.Exec(`UPDATE ai_eder_service_config SET default_context_tokens=? WHERE config_id=1`, bad); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.ederServiceConfig(context.Background()); err == nil {
				t.Fatal("invalid default context accepted")
			}
			w := httptest.NewRecorder()
			s.ederServiceConfig(w, httptest.NewRequest(http.MethodGet, "/v1/service/config", nil))
			if w.Code != 503 {
				t.Fatalf("invalid config status=%d", w.Code)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("configuration called provider")
	}
}

func TestEderDefaultsChatForwardingAndExplicitOverrides(t *testing.T) {
	for _, format := range []string{"reasoning_effort", "deepseek"} {
		t.Run(format, func(t *testing.T) {
			var captured map[string]json.RawMessage
			s, uid, model, calls := contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
				captured = nil
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
			})
			setIsolatedChatDefaults(t, s, "max", 65536)
			if _, err := s.store.db.Exec(`UPDATE ai_model SET allow_thinking=1,reasoning_efforts='["none","low","high"]',reasoning_format=? WHERE model_id=?`, format, model); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name, target, want string
				fields             map[string]any
			}{
				{"server clamps max", "max", "high", map[string]any{}},
				{"server refreshes default", "low", "low", map[string]any{}},
				{"explicit high preserved", "low", "high", map[string]any{"reasoning_effort": "high"}},
				{"explicit none preserved", "max", "none", map[string]any{"reasoning_effort": "none"}},
				{"explicit disabled preserved", "max", "none", map[string]any{"thinking": map[string]string{"type": "disabled"}}},
				{"null thinking uses default", "max", "high", map[string]any{"thinking": nil}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if _, err := s.store.db.Exec(`UPDATE ai_eder_service_config SET default_reasoning_effort=? WHERE config_id=1`, tc.target); err != nil {
						t.Fatal(err)
					}
					w := reasoningChat(t, s, uid, model, tc.fields)
					if w.Code != 200 {
						t.Fatalf("chat status=%d: %s", w.Code, w.Body.String())
					}
					if format == "deepseek" && tc.want == "none" {
						if string(captured["thinking"]) != `{"type":"disabled"}` || captured["reasoning_effort"] != nil {
							t.Fatal("default overrode explicit disable")
						}
					} else if string(captured["reasoning_effort"]) != `"`+tc.want+`"` {
						t.Fatalf("forwarded effort=%s want=%s", captured["reasoning_effort"], tc.want)
					}
				})
			}
			before := calls.Load()
			if w := reasoningChat(t, s, uid, model, map[string]any{"reasoning_effort": "max"}); w.Code != 403 {
				t.Fatalf("unsupported explicit effort status=%d", w.Code)
			}
			if calls.Load() != before {
				t.Fatal("unsupported explicit effort reached provider")
			}
		})
	}
}

func TestEderDefaultsStartupRequiresBothColumns(t *testing.T) {
	dsn := os.Getenv("AI_WALLET_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated wallet test database not configured")
	}
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, column := range []string{"default_reasoning_effort", "default_context_tokens"} {
		t.Run(column, func(t *testing.T) {
			if _, err := store.db.Exec(`ALTER TABLE ai_eder_service_config RENAME COLUMN ` + column + ` TO test_hidden_default`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := store.db.Exec(`ALTER TABLE ai_eder_service_config RENAME COLUMN test_hidden_default TO ` + column); err != nil {
					t.Error(err)
				}
			})
			opened, err := OpenStore(dsn)
			if opened != nil {
				opened.Close()
				t.Fatal("relay accepted missing default column")
			}
			if err == nil || !strings.Contains(err.Error(), "020") {
				t.Fatalf("wrong migration failure: %v", err)
			}
		})
	}
}
