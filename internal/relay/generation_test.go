package relay

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenerationAmountAndProtocol(t *testing.T) {
	for _, tc := range []struct {
		name  string
		price int64
		units int
		want  int64
		bad   bool
	}{
		{"one image", 1250000, 1, 1250000, false}, {"multiple images", 1250000, 4, 5000000, false},
		{"video seconds", 120000, 8, 960000, false}, {"micro precision", 1, 12, 12, false},
		{"zero price", 0, 1, 0, true}, {"negative price", -1, 1, 0, true}, {"zero units", 1, 0, 0, true},
		{"negative units", 1, -1, 0, true}, {"overflow", math.MaxInt64, 2, 0, true}, {"too many", 1, 31, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, e := generationAmount(tc.price, tc.units)
			if (e != nil) != tc.bad || !tc.bad && got != tc.want {
				t.Fatalf("amount=%d err=%v", got, e)
			}
		})
	}
	for _, tc := range []struct {
		kind, api string
		want      bool
	}{{"image", "openai-images", true}, {"image", "ark-images", true}, {"video", "openai-video", true}, {"video", "ark-video", true}, {"text", "openai-images", false}, {"image", "openai-video", false}, {"video", "ark-images", false}, {"image", "", false}} {
		if got := validGenerationAPI(tc.kind, tc.api); got != tc.want {
			t.Errorf("%s %s=%t", tc.kind, tc.api, got)
		}
	}
}

func TestGenerationEndpointAndAssets(t *testing.T) {
	for _, base := range []string{"https://vendor.invalid/v1", "https://vendor.invalid/v1/"} {
		got, e := generationEndpoint(base, "/images/generations")
		if e != nil || got != "https://vendor.invalid/v1/images/generations" {
			t.Fatalf("endpoint=%s err=%v", got, e)
		}
	}
	for _, base := range []string{"", "file:///tmp/secret", "https://user:pass@vendor.invalid", "https://vendor.invalid?x=1", "https://vendor.invalid#fragment", "https://%"} {
		if _, e := generationEndpoint(base, "/videos"); e == nil {
			t.Errorf("accepted invalid base %s", base)
		}
	}
	png := strings.SplitN(testImageURL(t, "png", 8, 8), ",", 2)[1]
	for _, tc := range []struct {
		name string
		a    generationAsset
		want bool
	}{{"https", generationAsset{URL: "https://cdn.invalid/image.png"}, true}, {"png", generationAsset{Base64: png}, true}, {"jpeg", generationAsset{Base64: strings.SplitN(testImageURL(t, "jpeg", 8, 8), ",", 2)[1]}, true}, {"truncated png", generationAsset{Base64: base64.StdEncoding.EncodeToString([]byte{137, 80, 78, 71, 13, 10, 26, 10, 0})}, false}, {"empty", generationAsset{}, false}, {"http", generationAsset{URL: "http://cdn.invalid/a"}, false}, {"credentials", generationAsset{URL: "https://secret@cdn.invalid/a"}, false}, {"invalid base64", generationAsset{Base64: "%%%"}, false}, {"both", generationAsset{URL: "https://cdn.invalid/a", Base64: png}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			if validGenerationAsset(tc.a) != tc.want {
				t.Fatal("asset validation mismatch")
			}
		})
	}
}

func generationFixture(t *testing.T, kind, api string, upstream http.HandlerFunc) (*Server, string, string, *atomic.Int32) {
	t.Helper()
	s, uid, model, calls := contextPolicyServer(t, upstream)
	if _, e := s.store.db.Exec(`UPDATE ai_model SET model_type=?,generation_api=?,generation_size=? WHERE model_id=?`, kind, api, map[string]string{"image": "1024x1024", "video": "720p"}[kind], model); e != nil {
		t.Fatal(e)
	}
	if _, e := s.store.db.Exec(`UPDATE ai_model_pricing SET generation_price_micro_per_unit=100,peak_generation_price_micro_per_unit=200,limited_generation_price_micro_per_unit=50 WHERE model_id=?`, model); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := s.store.db.Exec(`DELETE FROM ai_generation WHERE uid=?`, uid); e != nil {
			t.Error(e)
		}
	})
	return s, uid, model, calls
}

func generationCall(t *testing.T, s *Server, uid, model, id string, units int, mutate func(map[string]any)) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"model": model, "prompt": "test generation", "units": units, "client_info": map[string]any{"eder_version": "isolated-1", "software_version": "isolated-1", "software_hash": strings.Repeat("a", 64), "system": "isolated test"}}
	if mutate != nil {
		mutate(body)
	}
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/v1/generations", strings.NewReader(string(raw))).WithContext(walletContext(uid, ""))
	r.Header.Set("X-Request-ID", id)
	w := httptest.NewRecorder()
	requestTrace(http.HandlerFunc(s.createGeneration)).ServeHTTP(w, r)
	return w
}
func generationWait(t *testing.T, s *Server, id, want string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if e := s.store.db.QueryRow(`SELECT status FROM ai_generation WHERE request_id=?`, id).Scan(&state); e != nil {
			t.Fatal(e)
		} else if state == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	var state, code string
	_ = s.store.db.QueryRow(`SELECT status,error_code FROM ai_generation WHERE request_id=?`, id).Scan(&state, &code)
	t.Fatalf("task state=%s code=%s want=%s", state, code, want)
}
func assertGenerationLedger(t *testing.T, s *Server, id, kind string, units int, unitPrice, charge int64) {
	t.Helper()
	var inputKind string
	var gotUnits int
	var gotPrice, gotCharge int64
	e := s.store.db.QueryRow(`SELECT p.billing_input_kind,p.generation_units,p.generation_unit_price_micro,u.charged_micro FROM ai_usage_pricing p JOIN ai_usage u ON u.request_id=p.request_id WHERE p.request_id=?`, id).Scan(&inputKind, &gotUnits, &gotPrice, &gotCharge)
	if e != nil || inputKind != kind+"_generation" || gotUnits != units || gotPrice != unitPrice || gotCharge != charge {
		t.Fatalf("snapshot %s/%d/%d/%d err=%v", inputKind, gotUnits, gotPrice, gotCharge, e)
	}
}

func TestGenerationImageBillingAndLockedPrice(t *testing.T) {
	for _, tc := range []struct {
		name, api, period string
		requested, actual int
		price             int64
	}{{"openai_multiple", "openai-images", "standard", 3, 2, 100}, {"ark_single", "ark-images", "peak", 1, 1, 200}, {"activity", "openai-images", "limited", 1, 1, 50}} {
		t.Run(tc.name, func(t *testing.T) {
			entered := make(chan struct{})
			proceed := make(chan struct{})
			var s *Server
			var model string
			s, uid, model, _ := generationFixture(t, "image", tc.api, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/images/generations" || r.Header.Get("Authorization") != "Bearer context-test-provider-secret" {
					t.Error("invalid image upstream route/auth")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid body")
				}
				if body["model"] != model || body["size"] != "1024x1024" {
					t.Error("wrong image model/size")
				}
				if tc.api == "openai-images" && body["n"] != float64(tc.requested) {
					t.Error("wrong image count")
				}
				if tc.api == "ark-images" && body["response_format"] != "url" {
					t.Error("missing Ark URL response")
				}
				close(entered)
				<-proceed
				assets := []generationAsset{}
				for i := 0; i < tc.actual; i++ {
					assets = append(assets, generationAsset{URL: fmt.Sprintf("https://cdn.invalid/image-%d.png", i)})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": assets})
			})
			defer func() {
				select {
				case <-proceed:
				default:
					close(proceed)
				}
			}()
			if tc.period == "peak" {
				if _, e := s.store.db.Exec(`UPDATE ai_model_pricing SET peak_enabled=1,peak_start='00:00',peak_end='23:59' WHERE model_id=?`, model); e != nil {
					t.Fatal(e)
				}
			}
			if tc.period == "limited" {
				if _, e := s.store.db.Exec(`UPDATE ai_model_pricing SET limited_enabled=1,limited_start_unix=?,limited_end_unix=? WHERE model_id=?`, time.Now().Unix()-30, time.Now().Unix()+60, model); e != nil {
					t.Fatal(e)
				}
			}
			id := newRequestID()
			w := generationCall(t, s, uid, model, id, tc.requested, nil)
			if w.Code != 200 {
				t.Fatalf("create %d %s", w.Code, w.Body.String())
			}
			<-entered
			assertBalance(t, s, uid, 1000000000-tc.price*int64(tc.requested))
			if _, e := s.store.db.Exec(`UPDATE ai_model_pricing SET generation_price_micro_per_unit=900000,peak_generation_price_micro_per_unit=900000,limited_generation_price_micro_per_unit=900000 WHERE model_id=?`, model); e != nil {
				t.Fatal(e)
			}
			close(proceed)
			generationWait(t, s, id, "completed")
			assertBalance(t, s, uid, 1000000000-tc.price*int64(tc.actual))
			assertGenerationLedger(t, s, id, "image", tc.actual, tc.price, tc.price*int64(tc.actual))
			r := httptest.NewRequest("GET", "/v1/generations/"+id, nil)
			r.SetPathValue("id", id)
			r = r.WithContext(walletContext(uid, ""))
			out := httptest.NewRecorder()
			s.getGeneration(out, r)
			if out.Code != 200 || !strings.Contains(out.Body.String(), `"charged_credit"`) {
				t.Fatalf("result %d %s", out.Code, out.Body.String())
			}
		})
	}
}

func TestGenerationFailuresRefundAndReject(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		status         int
	}{{"upstream_rejected", `{"error":{"message":"rejected"}}`, 503}, {"malformed_json", `{`, 200}, {"empty_images", `{"data":[]}`, 200}, {"invalid_asset", `{"data":[{"url":"file:///etc/passwd"}]}`, 200}, {"too_many_images", `{"data":[{"url":"https://cdn.invalid/a"},{"url":"https://cdn.invalid/b"}]}`, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			s, uid, model, _ := generationFixture(t, "image", "openai-images", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.response))
			})
			id := newRequestID()
			w := generationCall(t, s, uid, model, id, 1, nil)
			if w.Code != 200 {
				t.Fatalf("submit %d %s", w.Code, w.Body.String())
			}
			generationWait(t, s, id, "failed")
			assertBalance(t, s, uid, 1000000000)
			var n int
			_ = s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_usage WHERE request_id=?`, id).Scan(&n)
			if n != 0 {
				t.Fatal("failed generation billed")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		units  int
		mutate func(map[string]any)
	}{{"zero", 0, nil}, {"too many", 5, nil}, {"empty prompt", 1, func(m map[string]any) { m["prompt"] = " " }}, {"missing client", 1, func(m map[string]any) { delete(m, "client_info") }}, {"unknown field", 1, func(m map[string]any) { m["api_key"] = "must not accept" }}} {
		t.Run(tc.name, func(t *testing.T) {
			s, uid, model, calls := generationFixture(t, "image", "openai-images", func(http.ResponseWriter, *http.Request) { t.Error("invalid request called supplier") })
			w := generationCall(t, s, uid, model, newRequestID(), tc.units, tc.mutate)
			if w.Code < 400 {
				t.Fatalf("invalid request accepted %d", w.Code)
			}
			assertBalance(t, s, uid, 1000000000)
			if calls.Load() != 0 {
				t.Fatal("upstream called")
			}
		})
	}
}

func TestGenerationRequestIdempotencyAndOwnership(t *testing.T) {
	s, uid, model, calls := generationFixture(t, "image", "openai-images", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"url":"https://cdn.invalid/image.png"}]}`))
	})
	id := newRequestID()
	w := generationCall(t, s, uid, model, id, 1, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	generationWait(t, s, id, "completed")
	for i := 0; i < 2; i++ {
		w = generationCall(t, s, uid, model, id, 1, nil)
		if w.Code != 200 {
			t.Fatal("retry did not return existing result")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate external generation")
	}
	assertBalance(t, s, uid, 999999900)
	w = generationCall(t, s, uid, model, id, 1, func(m map[string]any) { m["prompt"] = "different prompt" })
	if w.Code != 409 {
		t.Fatalf("id conflict %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/v1/generations/"+id, nil)
	r.SetPathValue("id", id)
	r = r.WithContext(walletContext("1", ""))
	out := httptest.NewRecorder()
	s.getGeneration(out, r)
	if out.Code != 404 {
		t.Fatal("cross-user result exposed")
	}
}

func TestGenerationVideoAsyncBilling(t *testing.T) {
	for _, api := range []string{"ark-video", "openai-video"} {
		t.Run(api, func(t *testing.T) {
			var polls atomic.Int32
			var model string
			s, uid, model, calls := generationFixture(t, "video", api, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/content") {
					if r.Header.Get("Authorization") != "Bearer context-test-provider-secret" {
						t.Error("video download used changed credential")
					}
					_, _ = w.Write([]byte("test mp4 content"))
					return
				}
				if r.Method == "POST" {
					if api == "ark-video" {
						var body map[string]any
						_ = json.NewDecoder(r.Body).Decode(&body)
						if r.URL.Path != "/contents/generations/tasks" || body["duration"] != float64(8) || body["resolution"] != "720p" || body["model"] != model {
							t.Error("invalid Ark create")
						}
					} else {
						if e := r.ParseMultipartForm(1 << 20); e != nil || r.URL.Path != "/videos" || r.FormValue("seconds") != "8" || r.FormValue("model") != model {
							t.Error("invalid OpenAI video multipart")
						}
					}
					_, _ = w.Write([]byte(`{"id":"task_test"}`))
					return
				}
				if polls.Add(1) == 1 {
					_, _ = w.Write([]byte(`{"status":"running"}`))
					return
				}
				if api == "ark-video" {
					_, _ = w.Write([]byte(`{"status":"succeeded","duration":6,"content":{"video_url":"https://cdn.invalid/video.mp4"}}`))
				} else {
					_, _ = w.Write([]byte(`{"status":"completed","seconds":"6"}`))
				}
			})
			id := newRequestID()
			w := generationCall(t, s, uid, model, id, 8, nil)
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			generationWait(t, s, id, "queued")
			assertBalance(t, s, uid, 999999200)
			s.pollGenerations(context.Background())
			if _, e := s.store.db.Exec(`UPDATE ai_generation SET next_poll_at=NOW(),lease_until=NULL WHERE request_id=?`, id); e != nil {
				t.Fatal(e)
			}
			s.pollGenerations(context.Background())
			generationWait(t, s, id, "completed")
			assertBalance(t, s, uid, 999999400)
			assertGenerationLedger(t, s, id, "video", 6, 100, 600)
			s.pollGenerations(context.Background())
			if calls.Load() != 3 {
				t.Fatalf("duplicate video processing calls=%d", calls.Load())
			}
			if api == "openai-video" {
				if _, e := s.store.db.Exec(`UPDATE ai_provider_credential SET secret_value='invalid-new-ciphertext' WHERE credential_id=(SELECT credential_id FROM ai_model WHERE model_id=?)`, model); e != nil {
					t.Fatal(e)
				}
				r := httptest.NewRequest("GET", "/v1/generations/"+id+"/content", nil).WithContext(walletContext(uid, ""))
				r.SetPathValue("id", id)
				out := httptest.NewRecorder()
				s.generationContent(out, r)
				if out.Code != 200 || out.Body.String() != "test mp4 content" {
					t.Fatalf("historical content used current model credential: %d %s", out.Code, out.Body.String())
				}
			}
		})
	}
}

func TestGenerationWalletRecordAtomicity(t *testing.T) {
	s, uid, model, _ := generationFixture(t, "image", "openai-images", func(http.ResponseWriter, *http.Request) { t.Error("wallet test called upstream") })
	ctx := walletContext(uid, "")
	id := newRequestID()
	if e := s.reserveWithRecord(ctx, uid, id, model, 100, 0, 0, 0, func(*sql.Tx) error { return errors.New("record failed") }); e == nil {
		t.Fatal("reserve record failure ignored")
	}
	assertBalance(t, s, uid, 1000000000)
	if e := s.reserve(ctx, uid, id, model, 100, 0, 0, 0); e != nil {
		t.Fatal(e)
	}
	rate := testWalletRate()
	rate.generationKind = "image"
	rate.generationPrice = 50
	rate.generationUnits = 1
	if e := s.settleWithRecord(ctx, uid, id, model, 0, 0, 100, 50, rate, func(*sql.Tx) error { return errors.New("record failed") }); e == nil {
		t.Fatal("settlement record failure ignored")
	}
	assertBalance(t, s, uid, 999999900)
	var n int
	_ = s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_usage WHERE request_id=?`, id).Scan(&n)
	if n != 0 {
		t.Fatal("partial settlement usage")
	}
	if e := s.refundWithRecord(ctx, uid, id, 100, func(*sql.Tx) error { return errors.New("record failed") }); e == nil {
		t.Fatal("refund record failure ignored")
	}
	assertBalance(t, s, uid, 999999900)
	if e := s.refund(ctx, uid, id, 100); e != nil {
		t.Fatal(e)
	}
	assertBalance(t, s, uid, 1000000000)
}

func TestGenerationAdmissionAndAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		want      int
	}{{"wrong product", `UPDATE ai_model SET product_scope='different' WHERE model_id=?`, 404}, {"disabled model", `UPDATE ai_model SET enabled=0 WHERE model_id=?`, 404}, {"unsupported category", `UPDATE ai_model SET model_type='text' WHERE model_id=?`, 503}, {"zero price", `UPDATE ai_model_pricing SET generation_price_micro_per_unit=0 WHERE model_id=?`, 503}} {
		t.Run(tc.name, func(t *testing.T) {
			s, uid, model, calls := generationFixture(t, "image", "openai-images", func(http.ResponseWriter, *http.Request) { t.Error("blocked model called upstream") })
			if _, e := s.store.db.Exec(tc.sql, model); e != nil {
				t.Fatal(e)
			}
			w := generationCall(t, s, uid, model, newRequestID(), 1, nil)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			assertBalance(t, s, uid, 1000000000)
			if calls.Load() != 0 {
				t.Fatal("upstream invoked")
			}
		})
	}
	t.Run("unauthenticated routes", func(t *testing.T) {
		s, _, _, _ := generationFixture(t, "image", "openai-images", func(http.ResponseWriter, *http.Request) { t.Error("unauthenticated call reached upstream") })
		s.cfg.JWTSecret = strings.Repeat("test", 12)
		for _, path := range []string{"/v1/generations", "/v1/generations/test", "/v1/generations/test/content"} {
			method := "GET"
			if path == "/v1/generations" {
				method = "POST"
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
			if w.Code != 401 {
				t.Fatalf("%s unauth status=%d", path, w.Code)
			}
		}
	})
	t.Run("service switch", func(t *testing.T) {
		s, uid, model, _ := generationFixture(t, "image", "openai-images", func(http.ResponseWriter, *http.Request) { t.Error("disabled service called upstream") })
		if _, e := s.store.db.Exec(`UPDATE ai_eder_service_config SET enabled=0 WHERE config_id=1`); e != nil {
			t.Fatal(e)
		}
		defer s.store.db.Exec(`UPDATE ai_eder_service_config SET enabled=1 WHERE config_id=1`)
		w := generationCall(t, s, uid, model, newRequestID(), 1, nil)
		if w.Code != 403 {
			t.Fatalf("service disabled status=%d", w.Code)
		}
		assertBalance(t, s, uid, 1000000000)
	})
	t.Run("suspended account", func(t *testing.T) {
		s, uid, model, calls := generationFixture(t, "image", "openai-images", func(http.ResponseWriter, *http.Request) { t.Error("suspended account called supplier") })
		if _, e := s.store.db.Exec(`UPDATE ai_account SET status='suspended',disabled_message='generation disabled' WHERE uid=?`, uid); e != nil {
			t.Fatal(e)
		}
		w := generationCall(t, s, uid, model, newRequestID(), 1, nil)
		if w.Code != 403 || !strings.Contains(w.Body.String(), `account_disabled`) {
			t.Fatalf("suspended account status=%d %s", w.Code, w.Body.String())
		}
		assertBalance(t, s, uid, 1000000000)
		if calls.Load() != 0 {
			t.Fatal("supplier called")
		}
	})
	t.Run("insufficient credit", func(t *testing.T) {
		s, uid, model, calls := generationFixture(t, "image", "openai-images", func(http.ResponseWriter, *http.Request) { t.Error("unfunded account called supplier") })
		if _, e := s.store.db.Exec(`UPDATE ai_wallet SET balance_micro=99 WHERE uid=?`, uid); e != nil {
			t.Fatal(e)
		}
		w := generationCall(t, s, uid, model, newRequestID(), 1, nil)
		if w.Code != 402 {
			t.Fatalf("unfunded account status=%d %s", w.Code, w.Body.String())
		}
		assertBalance(t, s, uid, 99)
		if calls.Load() != 0 {
			t.Fatal("supplier called")
		}
	})
}

func TestGenerationSettlementRecoveryDoesNotRegenerate(t *testing.T) {
	s, uid, model, calls := generationFixture(t, "image", "openai-images", func(http.ResponseWriter, *http.Request) { t.Error("recovery called a paid create endpoint") })
	id := newRequestID()
	j := generationJob{ID: id, UID: uid, Model: model, Kind: "image", API: "openai-images", Price: 100, Reservation: 100, Units: 1, Rate: testWalletRate()}
	e := s.reserveWithRecord(walletContext(uid, ""), uid, id, model, 100, 0, 0, 0, func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO ai_generation(request_id,uid,model_id,model_type,request_hash,status,generation_api,base_url,upstream_model,secret_value,requested_units,unit_price_micro,reservation_micro,price_kind,time_zone,price_locked_at) VALUES(?,?,?,'image',?,'submitting','openai-images','https://supplier.invalid','image-model','ciphertext',1,100,100,'standard','Asia/Shanghai',NOW())`, id, uid, model, strings.Repeat("a", 64))
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.store.db.Exec(`INSERT INTO ai_usage_pricing(request_id,uid,model_id,price_kind,time_zone,price_locked_at,input_price_micro_per_1k,cache_input_price_micro_per_1k,output_price_micro_per_1k,multiplier_bps) VALUES(?,?,?,'standard','Asia/Shanghai',NOW(),0,0,0,10000)`, id, uid, model)
	if e != nil {
		t.Fatal(e)
	}
	s.completeGeneration(j, 1, []generationAsset{{URL: "https://cdn.invalid/recover.png", MIME: "image/png"}})
	var state string
	var ready bool
	e = s.store.db.QueryRow(`SELECT status,result_json IS NOT NULL FROM ai_generation WHERE request_id=?`, id).Scan(&state, &ready)
	if e != nil || state != "queued" || !ready {
		t.Fatalf("failed settlement did not retain durable result: %s/%t %v", state, ready, e)
	}
	var usage int
	_ = s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_usage WHERE request_id=?`, id).Scan(&usage)
	if usage != 0 {
		t.Fatal("failed settlement committed usage")
	}
	assertBalance(t, s, uid, 999999900)
	if _, e = s.store.db.Exec(`DELETE FROM ai_usage_pricing WHERE request_id=?`, id); e != nil {
		t.Fatal(e)
	}
	restarted := NewServer(s.cfg, s.store)
	restarted.pollGenerations(context.Background())
	generationWait(t, s, id, "completed")
	assertGenerationLedger(t, s, id, "image", 1, 100, 100)
	assertBalance(t, s, uid, 999999900)
	restarted.pollGenerations(context.Background())
	if calls.Load() != 0 {
		t.Fatalf("recovery regenerated image calls=%d", calls.Load())
	}
}

func TestGenerationExpiredSubmissionRefundsOnce(t *testing.T) {
	s, uid, model, _ := generationFixture(t, "image", "openai-images", func(http.ResponseWriter, *http.Request) { t.Error("expired submission must not retry paid create") })
	id := newRequestID()
	ctx := walletContext(uid, "")
	e := s.reserveWithRecord(ctx, uid, id, model, 200, 0, 0, 0, func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO ai_generation(request_id,uid,model_id,model_type,request_hash,status,generation_api,base_url,upstream_model,secret_value,requested_units,unit_price_micro,reservation_micro,price_kind,time_zone,price_locked_at,created_at) VALUES(?,?,?,'image',?,'submitting','openai-images','https://supplier.invalid','image-model','ciphertext',2,100,200,'standard','Asia/Shanghai',NOW(),DATE_SUB(NOW(),INTERVAL 20 MINUTE))`, id, uid, model, strings.Repeat("a", 64))
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	assertBalance(t, s, uid, 999999800)
	s.pollGenerations(context.Background())
	generationWait(t, s, id, "failed")
	assertBalance(t, s, uid, 1000000000)
	s.pollGenerations(context.Background())
	assertBalance(t, s, uid, 1000000000)
	var n int
	_ = s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_wallet_ledger WHERE request_id=? AND reason='release'`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("refund count=%d", n)
	}
}

func TestGenerationSharedWalletUsesOwnThenParent(t *testing.T) {
	s, uid, model, _ := generationFixture(t, "image", "openai-images", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"url":"https://cdn.invalid/shared.png"}]}`))
	})
	parent := addWallet(t, s, 300)
	if _, e := s.store.db.Exec(`UPDATE ai_wallet SET balance_micro=20 WHERE uid=?`, uid); e != nil {
		t.Fatal(e)
	}
	id := newRequestID()
	body, _ := json.Marshal(generationRequest{Model: model, Prompt: "shared wallet test", Units: 1, ClientInfo: &ederClientInfo{EderVersion: "isolated-1", SoftwareVersion: "isolated-1", SoftwareHash: strings.Repeat("a", 64), System: "isolated test"}})
	r := httptest.NewRequest("POST", "/v1/generations", strings.NewReader(string(body))).WithContext(walletContext(uid, parent))
	r.Header.Set("X-Request-ID", id)
	w := httptest.NewRecorder()
	requestTrace(http.HandlerFunc(s.createGeneration)).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	generationWait(t, s, id, "completed")
	assertBalance(t, s, uid, 0)
	assertBalance(t, s, parent, 220)
	var own, shared int64
	if e := s.store.db.QueryRow(`SELECT -SUM(IF(uid=?,delta_micro,0)),-SUM(IF(uid=?,delta_micro,0)) FROM ai_wallet_ledger WHERE request_id=? AND reason IN('settlement','settlement_parent')`, uid, parent, id).Scan(&own, &shared); e != nil || own != 20 || shared != 80 {
		t.Fatalf("own=%d parent=%d err=%v", own, shared, e)
	}
}

func TestGenerationVideoFailureAndInvalidDurationRefund(t *testing.T) {
	for _, tc := range []struct{ name, response string }{{"failed", `{"status":"failed"}`}, {"missing duration", `{"status":"succeeded","content":{"video_url":"https://cdn.invalid/video.mp4"}}`}, {"more than reserved", `{"status":"succeeded","duration":20,"content":{"video_url":"https://cdn.invalid/video.mp4"}}`}, {"invalid video URL", `{"status":"succeeded","duration":5,"content":{"video_url":"file:///tmp/video.mp4"}}`}} {
		t.Run(tc.name, func(t *testing.T) {
			s, uid, model, _ := generationFixture(t, "video", "ark-video", func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					_, _ = w.Write([]byte(`{"id":"task_failed"}`))
				} else {
					_, _ = w.Write([]byte(tc.response))
				}
			})
			id := newRequestID()
			w := generationCall(t, s, uid, model, id, 8, nil)
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			generationWait(t, s, id, "queued")
			s.pollGenerations(context.Background())
			generationWait(t, s, id, "failed")
			assertBalance(t, s, uid, 1000000000)
		})
	}
}

func TestGenerationFullModelIDLength(t *testing.T) {
	s, uid, base, calls := generationFixture(t, "image", "openai-images", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"url":"https://cdn.invalid/long-id.png"}]}`))
	})
	model := strings.Repeat("g", 120)
	_, e := s.store.db.Exec(`INSERT INTO ai_model(model_id,display_name,provider_id,credential_id,upstream_model,product_scope,model_type,generation_api,generation_size,enabled) SELECT ?,display_name,provider_id,credential_id,upstream_model,product_scope,model_type,generation_api,generation_size,enabled FROM ai_model WHERE model_id=?`, model, base)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := s.store.db.Exec(`DELETE FROM ai_model WHERE model_id=?`, model); e != nil {
			t.Error(e)
		}
	})
	if _, e := s.store.db.Exec(`INSERT INTO ai_model_pricing(model_id,generation_price_micro_per_unit) VALUES(?,100)`, model); e != nil {
		t.Fatal(e)
	}
	id := newRequestID()
	w := generationCall(t, s, uid, model, id, 1, nil)
	if w.Code != 200 {
		t.Fatalf("valid 120-character model ID rejected: %d %s", w.Code, w.Body.String())
	}
	generationWait(t, s, id, "completed")
	assertBalance(t, s, uid, 999999900)
	if calls.Load() != 1 {
		t.Fatal("wrong generation count")
	}
}

func TestGenerationRedirectCannotReceiveCredential(t *testing.T) {
	var leaked atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Add(1)
		t.Error("generation followed a credential-bearing redirect")
	}))
	defer destination.Close()
	s, uid, model, _ := generationFixture(t, "image", "openai-images", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/steal", http.StatusTemporaryRedirect)
	})
	id := newRequestID()
	w := generationCall(t, s, uid, model, id, 1, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	generationWait(t, s, id, "failed")
	assertBalance(t, s, uid, 1000000000)
	if leaked.Load() != 0 {
		t.Fatal("provider credential exposed")
	}
}

func TestGenerationHistoryScopesOwnerAndOmitsAssets(t *testing.T) {
	s, uid, model, calls := generationFixture(t, "image", "openai-images", func(http.ResponseWriter, *http.Request) { t.Error("history called supplier") })
	other := addWallet(t, s, 300)
	for i := 0; i < 24; i++ {
		owner := uid
		if i == 23 {
			owner = other
		}
		id := fmt.Sprintf("history-%s-%02d", uid, i)
		_, e := s.store.db.Exec(`INSERT INTO ai_generation(request_id,uid,model_id,model_type,request_hash,status,generation_api,base_url,upstream_model,secret_value,requested_units,actual_units,unit_price_micro,reservation_micro,price_kind,time_zone,price_locked_at,result_json,created_at) VALUES(?,?,?,'image',?,'completed','openai-images','https://private-supplier.invalid','private-upstream-name',?,1,1,100,100,'standard','Asia/Shanghai',NOW(),?,DATE_ADD(NOW(),INTERVAL ? SECOND))`, id, owner, model, strings.Repeat("f", 64), []byte("private-credential-ciphertext"), `[{"b64_json":"private-output-base64","mime_type":"image/png"}]`, i)
		if e != nil {
			t.Fatal(e)
		}
	}
	t.Cleanup(func() {
		if _, e := s.store.db.Exec(`DELETE FROM ai_generation WHERE uid=?`, other); e != nil {
			t.Error(e)
		}
	})
	for _, tc := range []struct {
		owner        string
		count, first int
	}{{uid, 20, 22}, {other, 1, 23}} {
		r := httptest.NewRequest("GET", "/v1/generations", nil).WithContext(walletContext(tc.owner, ""))
		w := httptest.NewRecorder()
		s.listGenerations(w, r)
		if w.Code != 200 {
			t.Fatalf("history status=%d %s", w.Code, w.Body.String())
		}
		var body struct {
			Code int `json:"code"`
			Data struct {
				Items []map[string]json.RawMessage `json:"items"`
			} `json:"data"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		if body.Code != 0 || len(body.Data.Items) != tc.count {
			t.Fatalf("history count=%d want=%d", len(body.Data.Items), tc.count)
		}
		var first string
		_ = json.Unmarshal(body.Data.Items[0]["generation_id"], &first)
		if first != fmt.Sprintf("history-%s-%02d", uid, tc.first) {
			t.Fatalf("history scope/order first=%s", first)
		}
		for _, item := range body.Data.Items {
			if len(item) != 5 {
				t.Fatalf("unexpected history fields: %v", item)
			}
			for _, field := range []string{"generation_id", "model", "model_type", "status", "created_at"} {
				if _, ok := item[field]; !ok {
					t.Fatalf("missing %s", field)
				}
			}
		}
		for _, private := range []string{"b64_json", "result_json", "secret_value", "private-credential", "private-output", "private-supplier", "private-upstream", "request_hash"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("history leaked %s", private)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("history invoked upstream")
	}
}

func TestGenerationRetentionPreservesPendingAndWalletLedger(t *testing.T) {
	s, uid, model, calls := generationFixture(t, "image", "openai-images", func(http.ResponseWriter, *http.Request) { t.Error("retention called supplier") })
	for _, state := range []string{"completed", "failed", "queued", "submitting"} {
		_, e := s.store.db.Exec(`INSERT INTO ai_generation(request_id,uid,model_id,model_type,request_hash,status,generation_api,base_url,upstream_model,secret_value,requested_units,unit_price_micro,reservation_micro,price_kind,time_zone,price_locked_at,next_poll_at,lease_until,updated_at) VALUES(?,?,?,'image',?,?,'openai-images','https://supplier.invalid','image-model','encrypted',1,100,100,'standard','Asia/Shanghai',NOW(),DATE_ADD(NOW(),INTERVAL 1 DAY),DATE_ADD(NOW(),INTERVAL 1 DAY),DATE_SUB(NOW(),INTERVAL 31 DAY))`, newRequestID(), uid, model, strings.Repeat("a", 64), state)
		if e != nil {
			t.Fatal(e)
		}
	}
	quotaAddLedger(t, s, uid, "admin_recharge", 1000000000)
	var before int
	if e := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_wallet_ledger WHERE uid=?`, uid).Scan(&before); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.RunGenerations(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var terminal int
		e := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_generation WHERE uid=? AND status IN('completed','failed')`, uid).Scan(&terminal)
		if e != nil {
			t.Fatal(e)
		}
		if terminal == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired terminal generation results not removed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var pending, after int
	if e := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_generation WHERE uid=? AND status IN('queued','submitting')`, uid).Scan(&pending); e != nil || pending != 2 {
		t.Fatalf("pending=%d err=%v", pending, e)
	}
	if e := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_wallet_ledger WHERE uid=?`, uid).Scan(&after); e != nil || after != before {
		t.Fatalf("wallet ledger changed %d=>%d err=%v", before, after, e)
	}
	assertBalance(t, s, uid, 1000000000)
	if calls.Load() != 0 {
		t.Fatal("retention called upstream")
	}
}

