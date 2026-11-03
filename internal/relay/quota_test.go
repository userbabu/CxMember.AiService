package relay

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestQuotaUsedPercentRoundingAndRange(t *testing.T) {
	for _, tc := range []struct {
		name, total, used string
		want              float64
		null              bool
	}{
		{"no recharges", "0", "0", 0, true},
		{"usage without recharge", "0", "200", 0, true},
		{"zero usage", "100", "0", 0, false},
		{"one third", "3", "1", 33.33, false},
		{"one sixth", "6", "1", 16.67, false},
		{"below half point", "20001", "1", 0, false},
		{"exact half point", "20000", "1", 0.01, false},
		{"half up at 12 percent", "20000", "2469", 12.35, false},
		{"exact two decimals", "20000", "2468", 12.34, false},
		{"round reaches 100", "20000", "19999", 100, false},
		{"exactly all", "9", "9", 100, false},
		{"cap overspend", "9", "99", 100, false},
		{"above int64 total", "18446744073709551614", "9223372036854775807", 50, false},
		{"huge total and usage", "9999999999999999999999999999999999999999999999999999999999999999", "999999999999999999999999999999999999999999999999999999999999999", 10, false},
		{"huge usage cap", "1", "9999999999999999999999999999999999999999999999999999999999999999", 100, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := quotaUsedPercent(tc.total, tc.used)
			if err != nil {
				t.Fatal(err)
			}
			if tc.null {
				if actual != nil {
					t.Fatalf("percent=%v want=null", *actual)
				}
			} else if actual == nil || *actual != tc.want {
				t.Fatalf("percent=%v want=%v", actual, tc.want)
			}
		})
	}
}

func TestQuotaUsedPercentRejectsInvalidStatistics(t *testing.T) {
	for _, bad := range []string{"", "-1", "+1", "1.0", "0.1", " 1", "1 ", "1e4", "NaN", "null", "true", "１"} {
		for _, side := range []string{"recharged", "charged"} {
			t.Run(side+"/"+bad, func(t *testing.T) {
				total, used := "100", "1"
				if side == "recharged" {
					total = bad
				} else {
					used = bad
				}
				if _, err := quotaUsedPercent(total, used); err == nil {
					t.Fatalf("accepted invalid statistic %q", bad)
				}
			})
		}
	}
}

func quotaAddLedger(t *testing.T, s *Server, uid, reason string, delta int64) {
	t.Helper()
	// 历史充值的余额快照必须覆盖当前可用余额；累计充值仍只使用传入金额。
	var after int64
	if err := s.store.db.QueryRow(`SELECT balance_micro FROM ai_wallet WHERE uid=?`, uid).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if (reason == "admin_recharge" || reason == "order_recharge") && delta > after {
		after = delta
	}
	if _, err := s.store.db.Exec(`INSERT INTO ai_wallet_ledger(uid,request_id,delta_micro,balance_after_micro,reason) VALUES(?,?,?,?,?)`, uid, newRequestID(), delta, after, reason); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaRejectsBalanceWithoutRechargeBaseline(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, 1)
	code, _, body := quotaHTTP(t, s, uid, "")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"msg":"quota unavailable"`) {
		t.Fatalf("missing recharge baseline response=%d %s", code, body)
	}
}

func quotaHTTP(t *testing.T, s *Server, uid, parent string) (int, map[string]any, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/account/quota", nil).WithContext(walletContext(uid, parent))
	response := httptest.NewRecorder()
	s.quota(response, request)
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(response.Body.Bytes()))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.Code == 200 {
		if payload["code"] != json.Number("0") || payload["msg"] != "操作成功" || payload["errors"] != nil {
			t.Fatalf("invalid success envelope: %v", payload)
		}
		data, ok := payload["data"].(map[string]any)
		if !ok {
			t.Fatalf("quota data is %T", payload["data"])
		}
		payload = data
	}
	return response.Code, payload, response.Body.String()
}

func assertQuotaPercent(t *testing.T, payload map[string]any, want *float64) {
	t.Helper()
	value, exists := payload["used_percent"]
	if !exists {
		t.Fatal("used_percent field missing")
	}
	if want == nil {
		if value != nil {
			t.Fatalf("used_percent=%v want=null", value)
		}
		return
	}
	number, ok := value.(json.Number)
	if !ok {
		t.Fatalf("used_percent JSON type=%T want number", value)
	}
	got, err := number.Float64()
	if err != nil || got != *want {
		t.Fatalf("used_percent=%v want=%v err=%v", got, *want, err)
	}
}

func assertQuotaTotals(t *testing.T, payload map[string]any, total, used string) {
	t.Helper()
	for key, want := range map[string]string{"total_credit": total, "used_credit": used} {
		got, ok := payload[key].(string)
		if !ok || got != want {
			t.Fatalf("%s=%v (%T), want exact credit string %q", key, payload[key], payload[key], want)
		}
	}
}

func TestQuotaTotalsPreserveSixDecimalPrecision(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, 451920)
	quotaAddLedger(t, s, uid, "admin_recharge", 1000000000)
	quotaAddLedger(t, s, uid, "order_recharge", 1100000000)
	quotaAddLedger(t, s, uid, "settlement", -1324548080)
	quotaAddLedger(t, s, uid, "admin_deduction", -775000000)
	code, payload, body := quotaHTTP(t, s, uid, "")
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	assertQuotaTotals(t, payload, "0.45192", "2099.54808")
	if payload["balance_credit"] != "0.45192" {
		t.Fatalf("balance=%v", payload["balance_credit"])
	}

	precise := addWallet(t, s, 999999)
	quotaAddLedger(t, s, precise, "admin_recharge", 1000001)
	quotaAddLedger(t, s, precise, "settlement", -1)
	code, payload, body = quotaHTTP(t, s, precise, "")
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	assertQuotaTotals(t, payload, "0.999999", "0.000001")
}

func TestQuotaTotalsWithEmptyWalletAndLargeDeductions(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, 0)
	code, payload, body := quotaHTTP(t, s, uid, "")
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	assertQuotaTotals(t, payload, "0", "0")
	quotaAddLedger(t, s, uid, "settlement", math.MinInt64)
	quotaAddLedger(t, s, uid, "admin_deduction", math.MinInt64)
	code, payload, body = quotaHTTP(t, s, uid, "")
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	assertQuotaTotals(t, payload, "0", "18446744073709.551616")
	assertQuotaPercent(t, payload, nil)
}

func TestQuotaUsageJSONNullZeroAndExcludedOperations(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, 0)
	code, payload, _ := quotaHTTP(t, s, uid, "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	assertQuotaPercent(t, payload, nil)
	assertQuotaTotals(t, payload, "0", "0")
	quotaAddLedger(t, s, uid, "settlement", -1000000)
	code, payload, _ = quotaHTTP(t, s, uid, "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	assertQuotaPercent(t, payload, nil)
	assertQuotaTotals(t, payload, "0", "1")
	other := addWallet(t, s, 5000000)
	quotaAddLedger(t, s, other, "admin_recharge", 10000000)
	zero := 0.0
	code, payload, _ = quotaHTTP(t, s, other, "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	assertQuotaPercent(t, payload, &zero)
	assertQuotaTotals(t, payload, "5", "0")
	for _, entry := range []struct {
		reason string
		delta  int64
	}{{"reserve", -2000000}, {"reserve_parent", -3000000}, {"release", 2000000}, {"release_parent", 3000000}, {"admin_deduction", -4000000}} {
		quotaAddLedger(t, s, other, entry.reason, entry.delta)
	}
	code, payload, _ = quotaHTTP(t, s, other, "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	assertQuotaPercent(t, payload, &zero)
	assertQuotaTotals(t, payload, "5", "4")
	quotaAddLedger(t, s, other, "order_recharge", 10000000)
	quotaAddLedger(t, s, other, "settlement", -2000000)
	quotaAddLedger(t, s, other, "settlement_parent", -3000000)
	want := 25.0
	code, payload, _ = quotaHTTP(t, s, other, "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	assertQuotaPercent(t, payload, &want)
	assertQuotaTotals(t, payload, "5", "9")
	if payload["balance_credit"] != "5" || payload["self_balance_credit"] != "5" || payload["parent_balance_credit"] != "0" || payload["unit"] != "credit" {
		t.Fatalf("quota money contract changed: %v", payload)
	}
}

func TestQuotaUsageFollowsEligibleParentWalletScope(t *testing.T) {
	for _, state := range []string{"active", "suspended", "missing wallet", "missing account", "self"} {
		t.Run(state, func(t *testing.T) {
			s := walletServer(t)
			uid := addWallet(t, s, 7000000)
			parent := addWallet(t, s, 3000000)
			unrelated := addWallet(t, s, 1000000)
			quotaAddLedger(t, s, uid, "admin_recharge", 100000000)
			quotaAddLedger(t, s, uid, "settlement", -10000000)
			quotaAddLedger(t, s, parent, "order_recharge", 200000000)
			// 该父钱包为其他子账号承担的消费也属于其钱包历史，不能按当前聊天发起者过滤。
			quotaAddLedger(t, s, parent, "settlement_parent", -90000000)
			quotaAddLedger(t, s, unrelated, "admin_recharge", 1000000000)
			quotaAddLedger(t, s, unrelated, "settlement", -1000000000)
			want := 33.33
			wantBalance, wantParent := "10", "3"
			switch state {
			case "suspended":
				if _, err := s.store.db.Exec(`UPDATE ai_account SET status='suspended' WHERE uid=?`, parent); err != nil {
					t.Fatal(err)
				}
			case "missing wallet":
				if _, err := s.store.db.Exec(`DELETE FROM ai_wallet WHERE uid=?`, parent); err != nil {
					t.Fatal(err)
				}
			case "missing account":
				if _, err := s.store.db.Exec(`DELETE FROM ai_account WHERE uid=?`, parent); err != nil {
					t.Fatal(err)
				}
			case "self":
				parent = uid
			}
			if state != "active" {
				want = 10
				wantBalance, wantParent = "7", "0"
			}
			code, payload, body := quotaHTTP(t, s, uid, parent)
			if code != 200 {
				t.Fatalf("status=%d body=%s", code, body)
			}
			assertQuotaPercent(t, payload, &want)
			if state == "active" {
				assertQuotaTotals(t, payload, "10", "100")
			} else {
				assertQuotaTotals(t, payload, "7", "10")
			}
			if payload["balance_credit"] != wantBalance || payload["parent_balance_credit"] != wantParent {
				t.Fatalf("parent scope mismatch: %v", payload)
			}
		})
	}
}

func TestQuotaUsageHandlesLargeLedgerTotals(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, 1000000)
	quotaAddLedger(t, s, uid, "admin_recharge", math.MaxInt64)
	quotaAddLedger(t, s, uid, "order_recharge", math.MaxInt64)
	quotaAddLedger(t, s, uid, "settlement", math.MinInt64)
	want := 50.0
	code, payload, body := quotaHTTP(t, s, uid, "")
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	assertQuotaPercent(t, payload, &want)
	assertQuotaTotals(t, payload, "1", "9223372036854.775808")
	quotaAddLedger(t, s, uid, "settlement_parent", math.MinInt64)
	quotaAddLedger(t, s, uid, "settlement", -1)
	want = 100
	code, payload, body = quotaHTTP(t, s, uid, "")
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	assertQuotaPercent(t, payload, &want)
	assertQuotaTotals(t, payload, "1", "18446744073709.551617")
}

func TestQuotaUsageRejectsInvalidSignsAndBalanceOverflow(t *testing.T) {
	for _, tc := range []struct {
		reason string
		delta  int64
	}{{"admin_recharge", -1}, {"order_recharge", -1}, {"settlement", 1}, {"settlement_parent", 1}, {"admin_deduction", 1}} {
		t.Run(tc.reason, func(t *testing.T) {
			s := walletServer(t)
			uid := addWallet(t, s, 1000000)
			quotaAddLedger(t, s, uid, tc.reason, tc.delta)
			code, _, body := quotaHTTP(t, s, uid, "")
			if code != 503 || strings.Contains(body, "ledger") {
				t.Fatalf("invalid sign response=%d %s", code, body)
			}
		})
	}
	s := walletServer(t)
	uid := addWallet(t, s, math.MaxInt64)
	parent := addWallet(t, s, 1)
	code, _, body := quotaHTTP(t, s, uid, parent)
	if code != 503 {
		t.Fatalf("balance overflow response=%d %s", code, body)
	}
}

func TestQuotaUsageSQLFailureReturnsUnavailable(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, 1000000)
	if _, err := s.store.db.Exec(`RENAME TABLE ai_wallet_ledger TO ai_wallet_ledger_quota_test_hidden`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.store.db.Exec(`RENAME TABLE ai_wallet_ledger_quota_test_hidden TO ai_wallet_ledger`); err != nil {
			t.Error(err)
		}
	}()
	code, _, body := quotaHTTP(t, s, uid, "")
	if code != 503 || strings.Contains(body, "ai_wallet_ledger") || strings.Contains(body, "doesn't exist") {
		t.Fatalf("SQL failure response=%d %s", code, body)
	}
}

// 保留真实MySQL驱动和生产查询，仅在余额SELECT建立读视图后让实际结算事务提交。
type quotaSnapshotConnector struct {
	inner    driver.Connector
	observed chan struct{}
	proceed  chan struct{}
	once     sync.Once
}

func (c *quotaSnapshotConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &quotaSnapshotConn{Conn: conn, owner: c}, nil
}
func (c *quotaSnapshotConnector) Driver() driver.Driver { return c.inner.Driver() }

type quotaSnapshotConn struct {
	driver.Conn
	owner *quotaSnapshotConnector
}

func (c *quotaSnapshotConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}
func (c *quotaSnapshotConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err == nil && strings.Contains(query, "SELECT w.balance_micro,a.status,a.disabled_message,pw.uid") {
		c.owner.once.Do(func() {
			close(c.owner.observed)
			select {
			case <-c.owner.proceed:
			case <-ctx.Done():
			}
		})
	}
	return rows, err
}

func TestQuotaUsageSharesSnapshotWithConcurrentSettlement(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, 8000000)
	quotaAddLedger(t, s, uid, "admin_recharge", 10000000)
	quotaAddLedger(t, s, uid, "settlement", -2000000)
	config, err := mysql.ParseDSN(os.Getenv("AI_WALLET_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	// 测试连接启用驱动参数插值，确保真实QueryContext经过此观测点。
	config.InterpolateParams = true
	inner, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatal(err)
	}
	instrumented := &quotaSnapshotConnector{inner: inner, observed: make(chan struct{}), proceed: make(chan struct{})}
	reader := sql.OpenDB(instrumented)
	defer reader.Close()
	snapshotServer := NewServer(Config{}, &Store{db: reader})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type outcome struct {
		snapshot quotaSnapshot
		err      error
	}
	completed := make(chan outcome, 1)
	go func() {
		snapshot, err := snapshotServer.quotaSnapshot(ctx, uid, "")
		completed <- outcome{snapshot, err}
	}()
	var release sync.Once
	defer release.Do(func() { close(instrumented.proceed) })
	select {
	case <-instrumented.observed:
	case <-time.After(3 * time.Second):
		t.Fatal("production balance SELECT was not observed")
	}
	// 独立连接提交一笔真实结算；第一条SELECT已建立的RR快照必须继续看到旧账本。
	writer, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err = writer.ExecContext(ctx, `UPDATE ai_wallet SET balance_micro=5000000 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.ExecContext(ctx, `INSERT INTO ai_wallet_ledger(uid,request_id,delta_micro,balance_after_micro,reason) VALUES(?,?, -3000000,5000000,'settlement')`, uid, newRequestID()); err != nil {
		t.Fatal(err)
	}
	if err = writer.Commit(); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(instrumented.proceed) })
	result := <-completed
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.snapshot.self != 8000000 || result.snapshot.usedPercent == nil || *result.snapshot.usedPercent != 20 || result.snapshot.totalCredit != "8" || result.snapshot.usedCredit != "2" {
		t.Fatalf("mixed read snapshot: balance=%d percent=%v", result.snapshot.self, result.snapshot.usedPercent)
	}
	updated, err := s.quotaSnapshot(ctx, uid, "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.self != 5000000 || updated.usedPercent == nil || *updated.usedPercent != 50 || updated.totalCredit != "5" || updated.usedCredit != "5" {
		t.Fatal(fmt.Sprintf("new transaction missed committed settlement: %+v", updated))
	}
}

func TestQuotaUsageIgnoresActualSharedReservationsAndRefunds(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, 3000000)
	parent := addWallet(t, s, 7000000)
	quotaAddLedger(t, s, uid, "admin_recharge", 3000000)
	quotaAddLedger(t, s, parent, "order_recharge", 7000000)
	ctx := walletContext(uid, parent)
	request := newRequestID()
	if err := s.reserve(ctx, uid, request, "quota-reservation", 4000000, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	code, payload, body := quotaHTTP(t, s, uid, parent)
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	assertQuotaPercent(t, payload, &zero)
	assertQuotaTotals(t, payload, "6", "0")
	if payload["balance_credit"] != "6" {
		t.Fatalf("actual reservation did not freeze balances: %v", payload)
	}
	if err := s.refund(ctx, uid, request, 4000000); err != nil {
		t.Fatal(err)
	}
	code, payload, body = quotaHTTP(t, s, uid, parent)
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	assertQuotaPercent(t, payload, &zero)
	assertQuotaTotals(t, payload, "10", "0")
	if payload["balance_credit"] != "10" {
		t.Fatalf("actual failure refund did not restore balances: %v", payload)
	}
	request = newRequestID()
	if err := s.reserve(ctx, uid, request, "quota-reservation", 4000000, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	rate := testWalletRate()
	rate.input = 1000000
	rate.output = 0
	if err := s.settle(ctx, uid, request, "quota-reservation", 1000, 0, 4000000, 1000000, rate); err != nil {
		t.Fatal(err)
	}
	used := 10.0
	code, payload, body = quotaHTTP(t, s, uid, parent)
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	assertQuotaPercent(t, payload, &used)
	assertQuotaTotals(t, payload, "9", "1")
	if payload["balance_credit"] != "9" {
		t.Fatalf("settled balance mismatch: %v", payload)
	}
}
