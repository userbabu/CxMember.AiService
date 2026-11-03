package relay

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"math/big"
)

type quotaSnapshot struct {
	self, parent                  int64
	status                        string
	disabledMessage               string
	usedPercent, remainingPercent *float64
	quotaBaselineCredit           string
	totalCredit, usedCredit       string
}

// 余额、充值基准和账本统计共享只读快照，避免充值或扣费并发时混用不同时点的数据。
func (s *Server) quotaSnapshot(ctx context.Context, uid, parent string) (quotaSnapshot, error) {
	var result quotaSnapshot
	if parent == "" {
		parent = uid
	}
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var parentID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT w.balance_micro,a.status,a.disabled_message,pw.uid,COALESCE(pw.balance_micro,0)
 FROM ai_wallet w JOIN ai_account a ON a.uid=w.uid
 LEFT JOIN ai_account pa ON pa.uid=? AND pa.uid<>w.uid AND pa.status='active'
	LEFT JOIN ai_wallet pw ON pw.uid=pa.uid WHERE w.uid=?`, parent, uid).Scan(&result.self, &result.status, &result.disabledMessage, &parentID, &result.parent)
	if err != nil {
		return result, err
	}
	if result.self < 0 || result.parent < 0 || result.parent > math.MaxInt64-result.self {
		return result, errors.New("invalid quota balance")
	}
	// 只累计实际承担充值/消费的钱包；父钱包不可用时不计入其历史，主账号自身不重复累计。
	payer := uid
	if parentID.Valid {
		payer = parentID.String
	}
	selfBaseline, err := latestRechargeBaseline(ctx, tx, uid)
	if err != nil {
		return result, err
	}
	parentBaseline := int64(0)
	if parentID.Valid {
		parentBaseline, err = latestRechargeBaseline(ctx, tx, payer)
		if err != nil {
			return result, err
		}
	}
	if selfBaseline < result.self || parentBaseline < result.parent {
		return result, errors.New("quota recharge baseline is below wallet balance")
	}
	baseline := new(big.Int).Add(big.NewInt(selfBaseline), big.NewInt(parentBaseline))
	result.quotaBaselineCredit = formatCreditAmount(baseline)
	result.remainingPercent, err = quotaRemainingPercent(result.self+result.parent, baseline)
	if err != nil {
		return result, err
	}
	var recharged, charged, deducted string
	var invalid int64
	err = tx.QueryRowContext(ctx, `SELECT
 COALESCE(SUM(CASE WHEN reason IN ('admin_recharge','order_recharge') THEN delta_micro ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN reason IN ('settlement','settlement_parent') THEN -CAST(delta_micro AS DECIMAL(65,0)) ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN reason='admin_deduction' THEN -CAST(delta_micro AS DECIMAL(65,0)) ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN reason IN ('admin_recharge','order_recharge') AND delta_micro<0 THEN 1 WHEN reason IN ('settlement','settlement_parent','admin_deduction') AND delta_micro>0 THEN 1 ELSE 0 END),0)
 FROM ai_wallet_ledger WHERE uid=? OR uid=?`, uid, payer).Scan(&recharged, &charged, &deducted, &invalid)
	if err != nil {
		return result, err
	}
	if invalid != 0 {
		return result, errors.New("invalid quota ledger amount")
	}
	result.usedPercent, err = quotaUsedPercent(recharged, charged)
	if err != nil {
		return result, err
	}
	deductedAmount, err := parseQuotaStatistic(deducted)
	if err != nil {
		return result, err
	}
	// 已用包含真实结算与后台扣减；可用只取钱包余额，预扣期间也不虚增后续可用额度。
	usedAmount, _ := new(big.Int).SetString(charged, 10)
	usedAmount.Add(usedAmount, deductedAmount)
	result.totalCredit = formatCredit(result.self + result.parent)
	result.usedCredit = formatCreditAmount(usedAmount)
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func latestRechargeBaseline(ctx context.Context, tx *sql.Tx, uid string) (int64, error) {
	var baseline int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT balance_after_micro FROM ai_wallet_ledger
	WHERE uid=? AND reason IN ('admin_recharge','order_recharge') ORDER BY created_at DESC,ledger_id DESC LIMIT 1),0)`, uid).Scan(&baseline)
	if err != nil {
		return 0, err
	}
	if baseline < 0 {
		return 0, errors.New("invalid quota recharge baseline")
	}
	return baseline, nil
}

// 累计账本可能超过单个钱包的 Int64 范围，必须解析为非负任意精度整数。
func parseQuotaStatistic(value string) (*big.Int, error) {
	if value == "" {
		return nil, errors.New("missing quota statistic")
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return nil, errors.New("invalid quota statistic")
		}
	}
	n, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil, errors.New("invalid quota statistic")
	}
	return n, nil
}

// 百分比按整数基点四舍五入，零充值明确返回未定义。
func quotaUsedPercent(recharged, charged string) (*float64, error) {
	total, err := parseQuotaStatistic(recharged)
	if err != nil {
		return nil, err
	}
	used, err := parseQuotaStatistic(charged)
	if err != nil {
		return nil, err
	}
	if total.Sign() == 0 {
		return nil, nil
	}
	percent := 100.0
	if used.Cmp(total) < 0 {
		scaled := new(big.Int).Mul(used, big.NewInt(10000))
		points, rest := new(big.Int), new(big.Int)
		points.QuoRem(scaled, total, rest)
		if rest.Lsh(rest, 1).Cmp(total) >= 0 {
			points.Add(points, big.NewInt(1))
		}
		percent = float64(points.Int64()) / 100
	}
	return &percent, nil
}

func quotaRemainingPercent(balance int64, baseline *big.Int) (*float64, error) {
	if balance < 0 || baseline == nil || baseline.Sign() < 0 {
		return nil, errors.New("invalid quota remaining balance")
	}
	current := big.NewInt(balance)
	if current.Cmp(baseline) > 0 {
		return nil, errors.New("quota balance exceeds latest recharge baseline")
	}
	if baseline.Sign() == 0 {
		return nil, nil
	}
	scaled := new(big.Int).Mul(current, big.NewInt(10000))
	points, rest := new(big.Int), new(big.Int)
	points.QuoRem(scaled, baseline, rest)
	if rest.Lsh(rest, 1).Cmp(baseline) >= 0 {
		points.Add(points, big.NewInt(1))
	}
	returnPercent := float64(points.Int64()) / 100
	return &returnPercent, nil
}
