package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAllocateWalletPriority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		parts     []walletPart
		amount    int64
		charges   []int64
		wantError bool
	}{
		{"own first", []walletPart{{balance: 100}, {balance: 500}}, 60, []int64{60, 0}, false},
		{"parent supplements", []walletPart{{balance: 20}, {balance: 100}}, 60, []int64{20, 40}, false},
		{"parent only", []walletPart{{}, {balance: 100}}, 60, []int64{0, 60}, false},
		{"exact combined", []walletPart{{balance: 20}, {balance: 40}}, 60, []int64{20, 40}, false},
		{"insufficient combined", []walletPart{{balance: 20}, {balance: 39}}, 60, nil, true},
		{"zero", []walletPart{{balance: 20}, {balance: 40}}, 0, []int64{0, 0}, false},
		{"negative charge", []walletPart{{balance: 20}}, -1, nil, true},
		{"settlement restores reservation", []walletPart{{balance: 0, reserved: 20}, {balance: 60, reserved: 40}}, 30, []int64{20, 10}, false},
		{"restore overflow", []walletPart{{balance: math.MaxInt64, reserved: 1}}, 1, nil, true},
		{"maximum integer", []walletPart{{balance: math.MaxInt64}}, math.MaxInt64, []int64{math.MaxInt64}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := allocateWallet(tc.parts, tc.amount)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			if tc.wantError {
				return
			}
			for i, want := range tc.charges {
				if tc.parts[i].charge != want {
					t.Fatalf("wallet %d charge=%d want=%d", i, tc.parts[i].charge, want)
				}
			}
		})
	}
}

func TestParentUIDUsesAuthenticatedSubject(t *testing.T) {
	for _, tc := range []struct{ name, subject, parent, caller, want string }{
		{"child", "2", "1", "2", "1"},
		{"self", "1", "1", "1", ""},
		{"zero", "1", "0", "1", ""},
		{"missing", "1", "", "1", ""},
		{"different caller", "2", "1", "3", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), memberKey{}, Claims{Subject: tc.subject, ParentUID: tc.parent})
			if got := parentUID(ctx, tc.caller); got != tc.want {
				t.Fatalf("parent=%q want=%q", got, tc.want)
			}
		})
	}
}

var walletTestUID atomic.Uint64

func walletServer(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("AI_WALLET_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated wallet test database not configured")
	}
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return NewServer(Config{UpstreamTimeout: 5 * time.Second}, store)
}

func addWallet(t *testing.T, s *Server, balance int64) string {
	t.Helper()
	uid := fmt.Sprint(910000 + walletTestUID.Add(1))
	if _, err := s.store.db.Exec(`INSERT INTO ai_account(uid) VALUES(?)`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`INSERT INTO ai_wallet(uid,balance_micro) VALUES(?,?)`, uid, balance); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{`DELETE FROM ai_usage_pricing WHERE uid=?`, `DELETE FROM ai_usage WHERE uid=?`, `DELETE FROM ai_wallet_ledger WHERE uid=?`, `DELETE FROM ai_account WHERE uid=?`} {
			if _, err := s.store.db.Exec(query, uid); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	return uid
}

func walletContext(uid, parent string) context.Context {
	return context.WithValue(context.Background(), memberKey{}, Claims{Subject: uid, ParentUID: parent, Product: "17"})
}

func assertBalance(t *testing.T, s *Server, uid string, want int64) {
	t.Helper()
	var balance int64
	if err := s.store.db.QueryRow(`SELECT balance_micro FROM ai_wallet WHERE uid=?`, uid).Scan(&balance); err != nil || balance != want {
		t.Fatalf("uid=%s balance=%d want=%d err=%v", uid, balance, want, err)
	}
}

func testWalletRate() billingRate {
	return billingRate{kind: "standard", timeZone: "Asia/Shanghai", lockedAt: time.Now().UTC(), input: 1000, output: 1000, multiplier: 10000}
}

func TestSharedWalletSettlement(t *testing.T) {
	s := walletServer(t)
	parent, child := addWallet(t, s, 100), addWallet(t, s, 30)
	ctx := walletContext(child, parent)
	id := newRequestID()
	if err := s.reserve(ctx, child, id, "wallet-test", 40, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	assertBalance(t, s, child, 0)
	assertBalance(t, s, parent, 90)
	if err := s.settle(context.Background(), child, id, "wallet-test", 10, 10, 40, 20, testWalletRate()); err != nil {
		t.Fatal(err)
	}
	assertBalance(t, s, child, 10)
	assertBalance(t, s, parent, 100)
	var actor string
	var charged, ledgerNet int64
	if err := s.store.db.QueryRow(`SELECT CAST(uid AS CHAR),charged_micro FROM ai_usage WHERE request_id=?`, id).Scan(&actor, &charged); err != nil || actor != child || charged != 20 {
		t.Fatalf("usage actor=%s charge=%d err=%v", actor, charged, err)
	}
	if err := s.store.db.QueryRow(`SELECT SUM(delta_micro) FROM ai_wallet_ledger WHERE request_id=? AND reason IN ('settlement','settlement_parent')`, id).Scan(&ledgerNet); err != nil || ledgerNet != -20 {
		t.Fatalf("settled charge=%d err=%v", ledgerNet, err)
	}
	if err := s.refund(ctx, child, id, 40); err == nil {
		t.Fatal("settled reservation refunded again")
	}
	if err := s.settle(ctx, child, id, "wallet-test", 10, 10, 40, 20, testWalletRate()); err == nil {
		t.Fatal("duplicate settlement accepted")
	}
	assertBalance(t, s, child, 10)
	assertBalance(t, s, parent, 100)
}

func TestSharedWalletRejectsInsufficientAndUnavailableParent(t *testing.T) {
	s := walletServer(t)
	parent, child := addWallet(t, s, 39), addWallet(t, s, 20)
	ctx := walletContext(child, parent)
	if err := s.reserve(ctx, child, newRequestID(), "wallet-test", 60, 0, 0, 1); !errors.Is(err, errQuota) {
		t.Fatalf("error=%v", err)
	}
	assertBalance(t, s, child, 20)
	assertBalance(t, s, parent, 39)
	if _, err := s.store.db.Exec(`UPDATE ai_account SET status='suspended' WHERE uid=?`, parent); err != nil {
		t.Fatal(err)
	}
	if err := s.reserve(ctx, child, newRequestID(), "wallet-test", 30, 0, 0, 1); !errors.Is(err, errQuota) {
		t.Fatalf("suspended parent error=%v", err)
	}
	if err := s.reserve(walletContext(child, "999999999"), child, newRequestID(), "wallet-test", 30, 0, 0, 1); !errors.Is(err, errQuota) {
		t.Fatalf("missing parent error=%v", err)
	}
	var count int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_wallet_ledger WHERE uid=?`, child).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed request left ledger=%d err=%v", count, err)
	}
}

func TestSharedWalletRefundUsesOriginalOwners(t *testing.T) {
	s := walletServer(t)
	parent, child, other := addWallet(t, s, 100), addWallet(t, s, 20), addWallet(t, s, 500)
	id := newRequestID()
	if err := s.reserve(walletContext(child, parent), child, id, "wallet-test", 60, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`UPDATE ai_account SET status='suspended' WHERE uid=?`, parent); err != nil {
		t.Fatal(err)
	}
	if err := s.refund(walletContext(child, other), child, id, 60); err != nil {
		t.Fatal(err)
	}
	assertBalance(t, s, child, 20)
	assertBalance(t, s, parent, 100)
	assertBalance(t, s, other, 500)
	if err := s.refund(context.Background(), child, id, 60); err == nil {
		t.Fatal("duplicate refund accepted")
	}
	assertBalance(t, s, child, 20)
	assertBalance(t, s, parent, 100)
	// 客户端取消发生在预扣之后，退款必须使用独立数据库上下文。
	canceled, cancel := context.WithCancel(walletContext(child, parent))
	secondID := newRequestID()
	if err := s.reserve(canceled, child, secondID, "wallet-test", 10, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	s.release(canceled, child, secondID, 10)
	assertBalance(t, s, child, 20)
	assertBalance(t, s, parent, 100)
}

func TestWalletDuplicateReservationRollsBack(t *testing.T) {
	s := walletServer(t)
	parent, child := addWallet(t, s, 100), addWallet(t, s, 100)
	ctx, id := walletContext(child, parent), newRequestID()
	if err := s.reserve(ctx, child, id, "wallet-test", 30, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.reserve(ctx, child, id, "wallet-test", 30, 0, 0, 1); err == nil {
		t.Fatal("duplicate reservation accepted")
	}
	assertBalance(t, s, child, 70)
	assertBalance(t, s, parent, 100)
	if err := s.refund(ctx, parent, id, 30); err == nil {
		t.Fatal("wrong owner refunded reservation")
	}
	if err := s.refund(ctx, child, id, 31); err == nil {
		t.Fatal("wrong amount refunded reservation")
	}
	if err := s.refund(ctx, child, id, 30); err != nil {
		t.Fatal(err)
	}
	assertBalance(t, s, child, 100)
	assertBalance(t, s, parent, 100)
}

func TestWalletActualExceedsReservation(t *testing.T) {
	for _, enough := range []bool{true, false} {
		t.Run(fmt.Sprint(enough), func(t *testing.T) {
			s := walletServer(t)
			parent, child := addWallet(t, s, 100), addWallet(t, s, 20)
			ctx, id := walletContext(child, parent), newRequestID()
			if err := s.reserve(ctx, child, id, "wallet-test", 40, 0, 0, 1); err != nil {
				t.Fatal(err)
			}
			actual := int64(80)
			if !enough {
				actual = 121
			}
			err := s.settle(ctx, child, id, "wallet-test", 10, 10, 40, actual, testWalletRate())
			if enough {
				if err != nil {
					t.Fatal(err)
				}
				assertBalance(t, s, child, 0)
				assertBalance(t, s, parent, 40)
			} else {
				if !errors.Is(err, errQuota) {
					t.Fatalf("error=%v", err)
				}
				assertBalance(t, s, child, 0)
				assertBalance(t, s, parent, 80)
				if err = s.refund(ctx, child, id, 40); err != nil {
					t.Fatal(err)
				}
				assertBalance(t, s, child, 20)
				assertBalance(t, s, parent, 100)
			}
		})
	}
	t.Run("parent remains available after own-only reservation", func(t *testing.T) {
		s := walletServer(t)
		parent, child := addWallet(t, s, 100), addWallet(t, s, 100)
		ctx, id := walletContext(child, parent), newRequestID()
		if err := s.reserve(ctx, child, id, "wallet-test", 40, 0, 0, 1); err != nil {
			t.Fatal(err)
		}
		assertBalance(t, s, child, 60)
		assertBalance(t, s, parent, 100)
		if err := s.settle(walletContext(child, "999999999"), child, id, "wallet-test", 10, 10, 40, 150, testWalletRate()); err != nil {
			t.Fatal(err)
		}
		assertBalance(t, s, child, 0)
		assertBalance(t, s, parent, 50)
	})
}

func TestSharedWalletConcurrentChildren(t *testing.T) {
	s := walletServer(t)
	parent := addWallet(t, s, 100)
	children := make([]string, 10)
	for i := range children {
		children[i] = addWallet(t, s, 0)
	}
	var wg sync.WaitGroup
	var successes atomic.Int32
	errs := make(chan error, len(children))
	start := make(chan struct{})
	for _, child := range children {
		wg.Add(1)
		go func(uid string) {
			defer wg.Done()
			<-start
			err := s.reserve(walletContext(uid, parent), uid, newRequestID(), "wallet-test", 15, 0, 0, 1)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, errQuota) {
				errs <- err
			}
		}(child)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent reservation: %v", err)
	}
	if successes.Load() != 6 {
		t.Fatalf("successes=%d want=6", successes.Load())
	}
	assertBalance(t, s, parent, 10)
}

func TestWalletZeroAndNegativeBalances(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, 0)
	ctx, id := walletContext(uid, uid), newRequestID()
	if err := s.reserve(ctx, uid, id, "wallet-test", 0, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.refund(ctx, uid, id, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.reserve(ctx, uid, newRequestID(), "wallet-test", -1, 0, 0, 1); err == nil {
		t.Fatal("negative reservation accepted")
	}
	if _, err := s.store.db.Exec(`UPDATE ai_wallet SET balance_micro=-1 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	if err := s.reserve(ctx, uid, newRequestID(), "wallet-test", 0, 0, 0, 1); err == nil {
		t.Fatal("negative balance accepted")
	}
}

func TestQuotaIncludesOwnAndParent(t *testing.T) {
	s := walletServer(t)
	parent, child := addWallet(t, s, 100), addWallet(t, s, 20)
	quotaAddLedger(t, s, parent, "admin_recharge", 100)
	quotaAddLedger(t, s, child, "admin_recharge", 20)
	req := httptest.NewRequest(http.MethodGet, "/v1/account/quota", nil).WithContext(walletContext(child, parent))
	res := httptest.NewRecorder()
	s.quota(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("quota=%d %s", res.Code, res.Body.String())
	}
	var quota struct {
		Balance string `json:"balance_credit"`
		Own     string `json:"self_balance_credit"`
		Parent  string `json:"parent_balance_credit"`
	}
	if err := json.Unmarshal(testSuccessData(t, res.Body.Bytes()), &quota); err != nil {
		t.Fatal(err)
	}
	if quota.Balance != "0.00012" || quota.Own != "0.00002" || quota.Parent != "0.0001" {
		t.Fatalf("quota=%+v", quota)
	}
	selfReq := httptest.NewRequest(http.MethodGet, "/v1/account/quota", nil).WithContext(walletContext(child, child))
	selfRes := httptest.NewRecorder()
	s.quota(selfRes, selfReq)
	if err := json.Unmarshal(testSuccessData(t, selfRes.Body.Bytes()), &quota); err != nil {
		t.Fatal(err)
	}
	if selfRes.Code != http.StatusOK || quota.Balance != "0.00002" || quota.Parent != "0" {
		t.Fatalf("own wallet counted twice: %d %+v", selfRes.Code, quota)
	}
	if _, err := s.store.db.Exec(`UPDATE ai_account SET status='suspended' WHERE uid=?`, parent); err != nil {
		t.Fatal(err)
	}
	suspendedRes := httptest.NewRecorder()
	s.quota(suspendedRes, req)
	if err := json.Unmarshal(testSuccessData(t, suspendedRes.Body.Bytes()), &quota); err != nil {
		t.Fatal(err)
	}
	if suspendedRes.Code != http.StatusOK || quota.Balance != "0.00002" || quota.Parent != "0" {
		t.Fatalf("suspended parent included: %d %+v", suspendedRes.Code, quota)
	}
}

func TestWalletConcurrentFinalization(t *testing.T) {
	s := walletServer(t)
	parent, child := addWallet(t, s, 100), addWallet(t, s, 20)
	ctx, id := walletContext(child, parent), newRequestID()
	if err := s.reserve(ctx, child, id, "wallet-test", 60, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type result struct {
		settled bool
		err     error
	}
	results := make(chan result, 2)
	go func() { <-start; results <- result{false, s.refund(ctx, child, id, 60)} }()
	go func() {
		<-start
		results <- result{true, s.settle(ctx, child, id, "wallet-test", 10, 10, 60, 30, testWalletRate())}
	}()
	close(start)
	first, second := <-results, <-results
	if (first.err == nil) == (second.err == nil) {
		t.Fatalf("exactly one finalization must win: %v / %v", first.err, second.err)
	}
	winner := first
	if winner.err != nil {
		winner = second
	}
	if winner.settled {
		assertBalance(t, s, child, 0)
		assertBalance(t, s, parent, 90)
	} else {
		assertBalance(t, s, child, 20)
		assertBalance(t, s, parent, 100)
	}
}

func TestWalletRefundOverflowPreservesFunds(t *testing.T) {
	s := walletServer(t)
	uid := addWallet(t, s, math.MaxInt64)
	ctx, id := walletContext(uid, uid), newRequestID()
	if err := s.reserve(ctx, uid, id, "wallet-test", 1, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`UPDATE ai_wallet SET balance_micro=? WHERE uid=?`, int64(math.MaxInt64), uid); err != nil {
		t.Fatal(err)
	}
	if err := s.refund(ctx, uid, id, 1); err == nil {
		t.Fatal("overflowing refund accepted")
	}
	assertBalance(t, s, uid, math.MaxInt64)
	var count int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ai_wallet_ledger WHERE request_id=? AND reason='release'`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed refund left terminal ledger=%d err=%v", count, err)
	}
}

func TestWalletConcurrentDuplicateReservationAndSettlement(t *testing.T) {
	s := walletServer(t)
	parent, child := addWallet(t, s, 1000), addWallet(t, s, 1000)
	ctx := walletContext(child, parent)
	for i := 0; i < 10; i++ {
		id := newRequestID()
		if err := s.reserve(ctx, child, id, "wallet-test", 60, 0, 0, 1); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		duplicate, settled := make(chan error, 1), make(chan error, 1)
		go func() { <-start; duplicate <- s.reserve(ctx, child, id, "wallet-test", 60, 0, 0, 1) }()
		go func() { <-start; settled <- s.settle(ctx, child, id, "wallet-test", 10, 20, 60, 30, testWalletRate()) }()
		close(start)
		if err := <-duplicate; err == nil {
			t.Fatal("concurrent duplicate reservation accepted")
		}
		if err := <-settled; err != nil {
			t.Fatalf("duplicate request broke original settlement: %v", err)
		}
	}
	assertBalance(t, s, child, 700)
	assertBalance(t, s, parent, 1000)
}

func TestWalletConcurrentIndependentFinalizations(t *testing.T) {
	s := walletServer(t)
	type reservation struct{ uid, id string }
	requests := make([]reservation, 12)
	for i := range requests {
		uid, id := addWallet(t, s, 100), fmt.Sprintf("wallet-finalize-%020d", i)
		requests[i] = reservation{uid, id}
		if err := s.reserve(walletContext(uid, uid), uid, id, "wallet-test", 40, 0, 0, 1); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errs := make(chan error, len(requests))
	var wg sync.WaitGroup
	for _, req := range requests {
		wg.Add(1)
		go func(req reservation) {
			defer wg.Done()
			<-start
			errs <- s.settle(context.Background(), req.uid, req.id, "wallet-test", 10, 10, 40, 20, testWalletRate())
		}(req)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("independent finalization: %v", err)
		}
	}
	for _, req := range requests {
		assertBalance(t, s, req.uid, 80)
	}
}
