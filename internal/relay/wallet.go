package relay

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"
)

var errAccountWalletUnavailable = errors.New("account wallet unavailable")

type walletPart struct {
	uid      string
	balance  int64
	reserved int64
	charge   int64
}

// 主账号身份只取自会员服务签名的凭据，不能由聊天请求指定。
func parentUID(ctx context.Context, uid string) string {
	c, ok := ctx.Value(memberKey{}).(Claims)
	if ok && c.Subject == uid && c.ParentUID != uid && c.ParentUID != "0" {
		return c.ParentUID
	}
	return ""
}

// 固定 UID 顺序锁钱包，避免主账号和多个子账号并发时交叉加锁。
func lockWallets(ctx context.Context, tx *sql.Tx, uid, parent string) ([]walletPart, error) {
	if parent == "" {
		parent = uid
	}
	rows, err := tx.QueryContext(ctx, `SELECT CAST(w.uid AS CHAR),w.balance_micro FROM ai_wallet w JOIN ai_account a ON a.uid=w.uid WHERE (w.uid=? OR w.uid=?) AND a.status='active' ORDER BY w.uid FOR UPDATE`, uid, parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var self *walletPart
	var shared *walletPart
	for rows.Next() {
		var part walletPart
		if err = rows.Scan(&part.uid, &part.balance); err != nil {
			return nil, err
		}
		if part.balance < 0 {
			return nil, errors.New("negative wallet balance")
		}
		if part.uid == uid {
			self = &part
		} else {
			shared = &part
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if self == nil {
		return nil, errAccountWalletUnavailable
	}
	parts := []walletPart{*self}
	if shared != nil {
		parts = append(parts, *shared)
	}
	return parts, nil
}

// 本人额度先用完，再使用主账号额度；不足必须整笔失败。
func allocateWallet(parts []walletPart, amount int64) error {
	if amount < 0 {
		return errors.New("negative charge")
	}
	remaining := amount
	for i := range parts {
		if parts[i].balance < 0 || parts[i].reserved < 0 {
			return errors.New("negative wallet value")
		}
		available := parts[i].balance
		if parts[i].reserved > 0 && available > int64(^uint64(0)>>1)-parts[i].reserved {
			return errors.New("wallet overflow")
		}
		available += parts[i].reserved
		parts[i].charge = min(available, remaining)
		remaining -= parts[i].charge
	}
	if remaining != 0 {
		return errQuota
	}
	return nil
}

// 结算和退款按预扣流水定位付款钱包，主子关系变化也不会退错账户。
func reservedWallets(ctx context.Context, tx *sql.Tx, uid, requestID string, reservation int64) ([]walletPart, error) {
	rows, err := tx.QueryContext(ctx, `SELECT CAST(uid AS CHAR),-delta_micro,reason FROM ai_wallet_ledger WHERE request_id=? AND reason IN ('reserve','reserve_parent') ORDER BY reason`, requestID)
	if err != nil {
		return nil, err
	}
	var self, parent walletPart
	for rows.Next() {
		var p walletPart
		var reason string
		if err = rows.Scan(&p.uid, &p.reserved, &reason); err != nil {
			rows.Close()
			return nil, err
		}
		if p.reserved < 0 {
			rows.Close()
			return nil, errors.New("invalid reservation ledger")
		}
		if reason == "reserve" {
			self = p
		} else {
			parent = p
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if self.uid != uid || self.reserved > reservation || parent.reserved != reservation-self.reserved {
		return nil, errors.New("reservation owner or amount mismatch")
	}
	// 已冻结账户仍需接收原路退款，不能按当前启用状态丢弃预扣资金。
	parentID := parent.uid
	if parentID == "" {
		parentID = self.uid
	}
	locked, err := tx.QueryContext(ctx, `SELECT CAST(uid AS CHAR),balance_micro FROM ai_wallet WHERE uid=? OR uid=? ORDER BY uid FOR UPDATE`, self.uid, parentID)
	if err != nil {
		return nil, err
	}
	defer locked.Close()
	found := 0
	for locked.Next() {
		var id string
		var balance int64
		if err = locked.Scan(&id, &balance); err != nil {
			return nil, err
		}
		if balance < 0 {
			return nil, errors.New("negative wallet balance")
		}
		if id == self.uid {
			self.balance = balance
		} else {
			parent.balance = balance
		}
		found++
	}
	if err = locked.Err(); err != nil {
		return nil, err
	}
	parts := []walletPart{self}
	if parent.uid != "" {
		parts = append(parts, parent)
	}
	if found != len(parts) {
		return nil, errors.New("reserved wallet missing")
	}
	locked.Close()
	// 调用方使用 READ COMMITTED；锁住原钱包后读最新终态，避免旧快照和空范围间隙锁。
	var terminal string
	err = tx.QueryRowContext(ctx, `SELECT reason FROM ai_wallet_ledger WHERE request_id=? AND reason IN ('release','settlement') LIMIT 1`, requestID).Scan(&terminal)
	if err == nil {
		return nil, errors.New("reservation already finalized")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return parts, nil
}

func writeWalletResult(ctx context.Context, tx *sql.Tx, parts []walletPart, requestID, reason string) error {
	for i, p := range parts {
		if p.balance < 0 || p.reserved < 0 || p.balance > int64(^uint64(0)>>1)-p.reserved {
			return errors.New("wallet overflow")
		}
		balance := p.balance + p.reserved - p.charge
		if balance < 0 {
			return errQuota
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ai_wallet SET balance_micro=? WHERE uid=?`, balance, p.uid); err != nil {
			return err
		}
		entryReason := reason
		if i > 0 {
			entryReason += "_parent"
		}
		delta := p.reserved
		// 预扣是冻结记录；结算流水记录实际消费，余额快照已包含未消费部分的返还。
		if reason == "settlement" {
			delta = -p.charge
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_wallet_ledger(uid,request_id,delta_micro,balance_after_micro,reason) VALUES(?,?,?,?,?)`, p.uid, requestID, delta, balance, entryReason); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) release(ctx context.Context, uid, requestID string, amount int64) {
	// 请求取消不能取消退款；使用独立且有限的数据库上下文。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	err := s.refund(ctx, uid, requestID, amount)
	if err != nil {
		slog.Error("[AIRelay] wallet refund failed", "request_id", requestID, "error", err)
	}
}

func (s *Server) refund(ctx context.Context, uid, requestID string, amount int64) error {
	return s.refundWithRecord(ctx, uid, requestID, amount, nil)
}
func (s *Server) refundWithRecord(ctx context.Context, uid, requestID string, amount int64, record func(*sql.Tx) error) error {
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	parts, err := reservedWallets(ctx, tx, uid, requestID, amount)
	if err != nil {
		return err
	}
	if err = writeWalletResult(ctx, tx, parts, requestID, "release"); err != nil {
		return err
	}
	if record != nil {
		if err = record(tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}
