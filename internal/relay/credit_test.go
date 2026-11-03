package relay

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestCreditFormattingPreservesPrecision(t *testing.T) {
	for _, tc := range []struct {
		amount int64
		want   string
	}{
		{0, "0"}, {1, "0.000001"}, {1000, "0.001"}, {999999, "0.999999"},
		{1000000, "1"}, {1000000000, "1000"}, {1818869000, "1818.869"},
		{-1, "-0.000001"}, {-1200000, "-1.2"},
		{math.MaxInt64, "9223372036854.775807"}, {math.MinInt64, "-9223372036854.775808"},
	} {
		if got := formatCredit(tc.amount); got != tc.want {
			t.Fatalf("amount=%d got=%s want=%s", tc.amount, got, tc.want)
		}
	}
}

func TestCreditPriceRoundsAtMicrocredit(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		in, out, input, output int64
		multiplier             uint64
		want                   int64
		bad                    bool
	}{
		{"free", 100, 200, 0, 0, 10000, 0, false},
		{"sub microcredit", 1, 0, 1, 0, 10000, 1, false},
		{"exact microcredit", 1000, 0, 1, 0, 10000, 1, false},
		{"above microcredit", 1001, 0, 1, 0, 10000, 2, false},
		{"one model token", 1, 0, 320000, 0, 10000, 320, false},
		{"one million input", 1000000, 0, 320000, 0, 10000, 320000000, false},
		{"input plus output", 1000, 1000, 320000, 940000, 10000, 1260000, false},
		{"multiplier fractions", 1, 0, 1, 0, 1, 1, false},
		{"large safe result", 1000, 0, math.MaxInt64, 0, 10000, math.MaxInt64, false},
		{"sum overflow", 1000, 1000, math.MaxInt64, math.MaxInt64, 10000, 0, true},
		{"multiply overflow", math.MaxInt64, 0, math.MaxInt64, 0, math.MaxUint64, 0, true},
		{"negative tokens", -1, 0, 1, 0, 10000, 0, true},
		{"negative price", 1, 0, -1, 0, 10000, 0, true},
		{"zero multiplier", 1, 1, 1, 1, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := price(tc.in, tc.out, tc.input, tc.output, tc.multiplier)
			if (err != nil) != tc.bad || (!tc.bad && got != tc.want) {
				t.Fatalf("amount=%d error=%v want=%d bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestPublicCreditPricesAreExactStrings(t *testing.T) {
	rate := billingRate{kind: "peak", input: 320000, output: 940000, multiplier: 10000}
	data, err := rate.publicConsumption()
	if err != nil {
		t.Fatal(err)
	}
	if data["unit"] != "credit" || data["input_credit_per_1k"] != "0.32" || data["output_credit_per_1k"] != "0.94" {
		t.Fatalf("public price=%v", data)
	}
	if _, ok := data["input_multiplier"]; ok {
		t.Fatal("obsolete input multiplier leaked")
	}
	rate.input = 1
	rate.output = math.MaxInt64
	data, err = rate.publicConsumption()
	if err != nil || data["input_credit_per_1k"] != "0.000001" || data["output_credit_per_1k"] != "9223372036854.775807" {
		t.Fatalf("precise public price=%v err=%v", data, err)
	}
}

func TestCreditWalletAndUsageKeepEconomicValue(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, 1000*microPerCredit)
	quotaAddLedger(t, s, uid, "admin_recharge", 1000*microPerCredit)
	ctx, id := walletContext(uid, uid), newRequestID()
	rate := billingRate{kind: "standard", timeZone: "Asia/Shanghai", input: 320000, output: 940000, multiplier: 10000}
	rate.lockedAt = testWalletRate().lockedAt
	actual, err := price(1000, 1000, rate.input, rate.output, rate.multiplier)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.reserve(ctx, uid, id, "credit-test", 3*microPerCredit, 0, 0, 2000); err != nil {
		t.Fatal(err)
	}
	if err = s.settle(context.Background(), uid, id, "credit-test", 1000, 1000, 3*microPerCredit, actual, rate); err != nil {
		t.Fatal(err)
	}
	assertBalance(t, s, uid, 998740000)
	response := httptest.NewRecorder()
	s.quota(response, httptest.NewRequest(http.MethodGet, "/v1/account/quota", nil).WithContext(ctx))
	var quota map[string]interface{}
	if err = json.Unmarshal(testSuccessData(t, response.Body.Bytes()), &quota); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || quota["balance_credit"] != "998.74" || quota["unit"] != "credit" {
		t.Fatalf("quota=%d %v", response.Code, quota)
	}
	if _, ok := quota["balance_micro"]; ok {
		t.Fatal("internal microcredit leaked into quota API")
	}
	response = httptest.NewRecorder()
	s.usage(response, httptest.NewRequest(http.MethodGet, "/v1/account/usage", nil).WithContext(ctx))
	var usage []map[string]interface{}
	if err = json.Unmarshal(testSuccessData(t, response.Body.Bytes()), &usage); err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0]["charged_credit"] != "1.26" {
		t.Fatalf("usage=%s", response.Body.String())
	}
	if _, ok := usage[0]["charged_micro"]; ok {
		t.Fatal("internal microcredit leaked into usage API")
	}
}

func TestCreditStoreRejectsMissingOrWrongUnit(t *testing.T) {
	s := walletServer(t)
	var savedUnit string
	var savedScale int64
	if err := s.store.db.QueryRow(`SELECT unit,micro_per_credit FROM ai_billing_unit WHERE id=1`).Scan(&savedUnit, &savedScale); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.store.db.Exec(`INSERT INTO ai_billing_unit(id,unit,micro_per_credit) VALUES(1,?,?) ON DUPLICATE KEY UPDATE unit=VALUES(unit),micro_per_credit=VALUES(micro_per_credit)`, savedUnit, savedScale); err != nil {
			t.Error(err)
		}
	})
	dsn := os.Getenv("AI_WALLET_TEST_DSN")
	for _, query := range []string{`DELETE FROM ai_billing_unit WHERE id=1`, `INSERT INTO ai_billing_unit(id,unit,micro_per_credit) VALUES(1,'TOKEN',1000000)`, `UPDATE ai_billing_unit SET unit='credit',micro_per_credit=1000 WHERE id=1`} {
		if _, err := s.store.db.Exec(query); err != nil {
			t.Fatal(err)
		}
		store, err := OpenStore(dsn)
		if store != nil {
			store.Close()
		}
		if err == nil {
			t.Fatal("unmigrated or incorrect financial unit accepted")
		}
	}
}
