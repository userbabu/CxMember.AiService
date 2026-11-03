package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestReasoningPolicyValidation(t *testing.T) {
	all := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	raw, _ := json.Marshal(all)
	for _, format := range []string{"reasoning_effort", "deepseek"} {
		got, err := reasoningPolicy(string(raw), format, true)
		if err != nil || !reflect.DeepEqual(got, all) {
			t.Fatalf("format=%s policy=%v err=%v", format, got, err)
		}
		for _, stored := range []string{"[]", string(raw)} {
			got, err = reasoningPolicy(stored, format, false)
			if err != nil || got == nil || len(got) != 0 {
				t.Fatalf("disabled policy=%v err=%v", got, err)
			}
		}
	}
	for _, tc := range []struct {
		name, raw, format string
		enabled           bool
	}{
		{"empty enabled", "[]", "reasoning_effort", true},
		{"missing", "", "reasoning_effort", false},
		{"null", "null", "reasoning_effort", false},
		{"not array", "{}", "reasoning_effort", true},
		{"not string", `[1]`, "reasoning_effort", true},
		{"wrong case", `["High"]`, "reasoning_effort", true},
		{"unknown tier", `["turbo"]`, "reasoning_effort", true},
		{"duplicate", `["high","high"]`, "reasoning_effort", true},
		{"invalid disabled", `["bogus"]`, "reasoning_effort", false},
		{"invalid format", `["high"]`, "unknown", true},
		{"missing format", "[]", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := reasoningPolicy(tc.raw, tc.format, tc.enabled); err == nil {
				t.Fatal("invalid stored policy accepted")
			}
		})
	}
}

func TestSelectReasoningEnforcesWhitelist(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		allowed                []string
		effort, thinking, want string
		bad                    bool
	}{
		{"disabled default", nil, "", "", "none", false},
		{"disabled none", nil, "none", "", "none", false},
		{"disabled toggle", nil, "", `{"type":"disabled"}`, "none", false},
		{"disabled high", nil, "high", "", "", true},
		{"disabled enabled", nil, "", `{"type":"enabled"}`, "", true},
		{"default first", []string{"minimal", "high"}, "", "", "minimal", false},
		{"default none first", []string{"none", "high"}, "", "", "none", false},
		{"enabled chooses first thinking", []string{"none", "low", "high"}, "", `{"type":"enabled"}`, "low", false},
		{"enabled none only", []string{"none"}, "", `{"type":"enabled"}`, "", true},
		{"explicit high", []string{"none", "high"}, "high", `{"type":"enabled"}`, "high", false},
		{"explicit disabled", []string{"none", "high"}, "none", `{"type":"disabled"}`, "none", false},
		{"none missing", []string{"high"}, "", `{"type":"disabled"}`, "", true},
		{"tier missing", []string{"high"}, "low", "", "", true},
		{"conflicting high disabled", []string{"none", "high"}, "high", `{"type":"disabled"}`, "", true},
		{"conflicting none enabled", []string{"none", "high"}, "none", `{"type":"enabled"}`, "", true},
		{"wrong case", []string{"high"}, "HIGH", "", "", true},
		{"whitespace tier", []string{"high"}, " high ", "", "", true},
		{"unknown thinking property", []string{"high"}, "high", `{"type":"enabled","budget_tokens":1000}`, "", true},
		{"unknown mode", []string{"high"}, "", `{"type":"auto"}`, "", true},
		{"missing mode", []string{"high"}, "", `{}`, "", true},
		{"array thinking", []string{"high"}, "", `[]`, "", true},
		{"string thinking", []string{"high"}, "", `"enabled"`, "", true},
		{"boolean thinking", []string{"high"}, "", `true`, "", true},
		{"wrong mode type", []string{"high"}, "", `{"type":true}`, "", true},
		{"trailing object", []string{"high"}, "", `{"type":"enabled"}{}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectReasoning(chatRequest{ReasoningEffort: tc.effort, Thinking: json.RawMessage(tc.thinking)}, tc.allowed)
			if (err != nil) != tc.bad || !tc.bad && got != tc.want {
				t.Fatalf("selected=%s err=%v want=%s bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestReasoningUpstreamFormats(t *testing.T) {
	for _, format := range []string{"reasoning_effort", "deepseek"} {
		for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
			t.Run(format+"/"+effort, func(t *testing.T) {
				body := map[string]any{"model": "test"}
				applyReasoning(body, format, effort)
				if format == "reasoning_effort" {
					if body["reasoning_effort"] != effort || body["thinking"] != nil {
						t.Fatalf("unexpected request=%v", body)
					}
				} else {
					mode := "enabled"
					if effort == "none" {
						mode = "disabled"
					}
					if !reflect.DeepEqual(body["thinking"], map[string]string{"type": mode}) {
						t.Fatalf("unexpected thinking=%v", body)
					}
					if effort == "none" {
						if _, exists := body["reasoning_effort"]; exists {
							t.Fatal("disabled deepseek request contains effort")
						}
					} else if body["reasoning_effort"] != effort {
						t.Fatalf("missing effort: %v", body)
					}
				}
			})
		}
	}
}

func reasoningChat(t *testing.T, s *Server, uid, model string, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	fields["model"] = model
	fields["messages"] = []map[string]string{{"role": "user", "content": "hello"}}
	fields["max_tokens"] = 8
	fields["client_info"] = isolatedClientInfo()
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(walletContext(uid, ""))
	response := httptest.NewRecorder()
	requestTrace(http.HandlerFunc(s.chat)).ServeHTTP(response, request)
	return response
}

func TestReasoningChatForwardsAllAllowedTiers(t *testing.T) {
	for _, format := range []string{"reasoning_effort", "deepseek"} {
		t.Run(format, func(t *testing.T) {
			var forwarded map[string]json.RawMessage
			s, uid, model, calls := contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&forwarded); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
			})
			whitelist := `["none","minimal","low","medium","high","xhigh","max"]`
			if _, err := s.store.db.Exec(`UPDATE ai_model SET allow_thinking=1,reasoning_efforts=?,reasoning_format=? WHERE model_id=?`, whitelist, format, model); err != nil {
				t.Fatal(err)
			}
			for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
				response := reasoningChat(t, s, uid, model, map[string]any{"reasoning_effort": effort})
				if response.Code != 200 {
					t.Fatalf("effort=%s status=%d body=%s", effort, response.Code, response.Body.String())
				}
				if format == "deepseek" {
					want := `{"type":"enabled"}`
					if effort == "none" {
						want = `{"type":"disabled"}`
					}
					if string(forwarded["thinking"]) != want {
						t.Fatalf("effort=%s upstream=%s", effort, forwarded["thinking"])
					}
					if effort == "none" {
						if _, exists := forwarded["reasoning_effort"]; exists {
							t.Fatal("disabled request forwarded effort")
						}
					} else if string(forwarded["reasoning_effort"]) != `"`+effort+`"` {
						t.Fatal("incorrect effort")
					}
				} else if string(forwarded["reasoning_effort"]) != `"`+effort+`"` || forwarded["thinking"] != nil {
					t.Fatalf("upstream format mismatch: %v", forwarded)
				}
			}
			if calls.Load() != 7 {
				t.Fatalf("upstream calls=%d", calls.Load())
			}
		})
	}
}

func TestReasoningRejectedBeforeBillingAndRateWindow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		raw     string
		fields  map[string]any
		status  int
	}{
		{"not whitelisted", true, `["high"]`, map[string]any{"reasoning_effort": "low"}, 403},
		{"disabled", false, `["high"]`, map[string]any{"reasoning_effort": "high"}, 403},
		{"conflicting toggle", true, `["none","high"]`, map[string]any{"reasoning_effort": "high", "thinking": map[string]any{"type": "disabled"}}, 403},
		{"unknown argument", true, `["high"]`, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 4096}}, 403},
		{"invalid stored policy", true, `["bogus"]`, map[string]any{}, 503},
		{"empty enabled policy", true, `[]`, map[string]any{}, 503},
		{"missing stored policy", false, ``, map[string]any{}, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, uid, model, calls := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("rejected reasoning reached upstream") })
			if _, err := s.store.db.Exec(`UPDATE ai_model SET allow_thinking=?,reasoning_efforts=?,tpm=100,rpm=1 WHERE model_id=?`, tc.enabled, tc.raw, model); err != nil {
				t.Fatal(err)
			}
			response := reasoningChat(t, s, uid, model, tc.fields)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertBalance(t, s, uid, 1000000000)
			for _, table := range []string{"ai_wallet_ledger", "ai_usage_pricing"} {
				var count int
				if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE uid=?`, uid).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s count=%d err=%v", table, count, err)
				}
			}
			code, message := "permission_error", "the requested reasoning setting is not enabled for this model"
			if tc.status == http.StatusServiceUnavailable {
				code, message = "service_unavailable", "model reasoning configuration is invalid"
			}
			assertUnbilledChatFailure(t, s, uid, response.Header().Get("X-Request-ID"), code, message, tc.status)
			var count int
			if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_rate_window WHERE model_id=?`, model).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rate window count=%d err=%v", count, err)
			}
			assertFailureTrace(t, response)
			if calls.Load() != 0 {
				t.Fatal("rejected reasoning advanced into request execution")
			}
		})
	}
}

func TestReasoningCatalogAndQuotaAreConsistent(t *testing.T) {
	s, uid, model, _ := contextPolicyServer(t, func(http.ResponseWriter, *http.Request) { t.Error("catalog called upstream") })
	quotaAddLedger(t, s, uid, "admin_recharge", 1000000000)
	for _, tc := range []struct {
		name, raw, want string
		enabled         bool
	}{
		{"enabled", `["minimal","high","max"]`, `["minimal","high","max"]`, true},
		{"none only", `["none"]`, `["none"]`, true},
		{"disabled preserves stored tiers", `["minimal","high"]`, `[]`, false},
		{"disabled empty", `[]`, `[]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.store.db.Exec(`UPDATE ai_model SET allow_thinking=?,reasoning_efforts=? WHERE model_id=?`, tc.enabled, tc.raw, model); err != nil {
				t.Fatal(err)
			}
			for _, endpoint := range []string{"models", "quota"} {
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, "/v1/"+endpoint, nil).WithContext(walletContext(uid, ""))
				if endpoint == "models" {
					s.models(response, request)
				} else {
					s.quota(response, request)
				}
				if response.Code != 200 {
					t.Fatalf("catalog failed=%s", response.Body.String())
				}
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(testSuccessData(t, response.Body.Bytes()), &payload); err != nil {
					t.Fatal(err)
				}
				key := "data"
				if endpoint == "quota" {
					key = "pricing_models"
				}
				var entries []map[string]json.RawMessage
				if err := json.Unmarshal(payload[key], &entries); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, entry := range entries {
					if string(entry["id"]) == `"`+model+`"` {
						found = true
						if string(entry["reasoning_efforts"]) != tc.want {
							t.Fatalf("%s reasoning=%s want=%s", endpoint, entry["reasoning_efforts"], tc.want)
						}
					}
				}
				if !found {
					t.Fatal("model missing")
				}
			}
		})
	}
	if _, err := s.store.db.Exec(`UPDATE ai_model SET reasoning_efforts='null' WHERE model_id=?`, model); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publicModelPrices(context.Background(), "17"); err == nil {
		t.Fatal("invalid policy was published")
	}
	response := httptest.NewRecorder()
	s.models(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil).WithContext(walletContext(uid, "")))
	if response.Code != 503 || strings.Contains(response.Body.String(), "reasoning_efforts") {
		t.Fatalf("invalid policy response=%d %s", response.Code, response.Body.String())
	}
}

func TestReasoningChatDefaultsAndToggle(t *testing.T) {
	for _, format := range []string{"reasoning_effort", "deepseek"} {
		for _, tc := range []struct {
			name, raw, want string
			enabled         bool
			fields          map[string]any
		}{
			{"service default below allowed selects minimum", `["high","low"]`, "low", true, map[string]any{}},
			{"disabled clears upstream default", `["high","low"]`, "none", false, map[string]any{}},
			{"thinking only", `["none","high"]`, "high", true, map[string]any{"thinking": map[string]string{"type": "enabled"}}},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				var forwarded map[string]json.RawMessage
				s, uid, model, calls := contextPolicyServer(t, func(w http.ResponseWriter, r *http.Request) {
					if err := json.NewDecoder(r.Body).Decode(&forwarded); err != nil {
						t.Error(err)
					}
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
				})
				setIsolatedChatDefaults(t, s, "none", 65536)
				if _, err := s.store.db.Exec(`UPDATE ai_model SET allow_thinking=?,reasoning_efforts=?,reasoning_format=? WHERE model_id=?`, tc.enabled, tc.raw, format, model); err != nil {
					t.Fatal(err)
				}
				response := reasoningChat(t, s, uid, model, tc.fields)
				if response.Code != 200 {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				if format == "reasoning_effort" {
					if string(forwarded["reasoning_effort"]) != `"`+tc.want+`"` || forwarded["thinking"] != nil {
						t.Fatalf("unexpected upstream=%v", forwarded)
					}
				} else if tc.want == "none" {
					if string(forwarded["thinking"]) != `{"type":"disabled"}` || forwarded["reasoning_effort"] != nil {
						t.Fatalf("thinking not disabled: %v", forwarded)
					}
				} else if string(forwarded["thinking"]) != `{"type":"enabled"}` || string(forwarded["reasoning_effort"]) != `"`+tc.want+`"` {
					t.Fatalf("thinking default not respected: %v", forwarded)
				}
				if calls.Load() != 1 {
					t.Fatalf("upstream calls=%d", calls.Load())
				}
			})
		}
	}
}
