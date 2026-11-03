package relay

import (
	"database/sql"
	"testing"
	"time"
)

func TestScheduledPricingBoundaries(t *testing.T) {
	p := modelPricing{timeZone: "Asia/Shanghai", peakEnabled: true, peakStart: sql.NullString{String: "18:00", Valid: true}, peakEnd: sql.NullString{String: "23:00", Valid: true}, peakInput: 2000, peakOutput: 8000, peakMultiplier: 15000, limitedInput: 500, limitedOutput: 2000, limitedMultiplier: 5000}
	parse := func(value string) time.Time {
		v, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	p.limitedStart = sql.NullInt64{Int64: parse("2026-10-24T19:00:00+08:00").Unix(), Valid: true}
	p.limitedEnd = sql.NullInt64{Int64: parse("2026-10-24T20:00:00+08:00").Unix(), Valid: true}
	cases := []struct {
		name, at, kind string
		limited        bool
		input          int64
		multiplier     uint64
	}{
		{"before peak", "2026-10-24T17:59:59+08:00", "standard", false, 1000, 10000},
		{"peak start", "2026-10-24T18:00:00+08:00", "peak", false, 2000, 15000},
		{"peak end", "2026-10-24T23:00:00+08:00", "standard", false, 1000, 10000},
		{"UTC clock", "2026-10-24T10:00:00Z", "peak", false, 2000, 15000},
		{"activity start and priority", "2026-10-24T19:00:00+08:00", "limited", true, 500, 5000},
		{"activity end resumes peak", "2026-10-24T20:00:00+08:00", "peak", true, 2000, 15000},
		{"expired activity resumes standard", "2026-10-25T12:00:00+08:00", "standard", true, 1000, 10000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p.limitedEnabled = c.limited
			now := parse(c.at)
			r, err := p.resolve(now, 1000, 4000, 10000)
			if err != nil || r.kind != c.kind || r.input != c.input || r.multiplier != c.multiplier || !r.lockedAt.Equal(now) {
				t.Fatalf("rate=%+v err=%v", r, err)
			}
		})
	}
	p.limitedEnabled = false
	p.peakStart.String = "22:00"
	p.peakEnd.String = "06:00"
	for _, c := range []struct{ at, kind string }{{"2026-10-24T21:59:59+08:00", "standard"}, {"2026-10-24T22:00:00+08:00", "peak"}, {"2026-10-25T00:00:00+08:00", "peak"}, {"2026-10-25T05:59:59+08:00", "peak"}, {"2026-10-25T06:00:00+08:00", "standard"}} {
		r, err := p.resolve(parse(c.at), 1000, 4000, 10000)
		if err != nil || r.kind != c.kind {
			t.Fatalf("cross-midnight %s: %+v %v", c.at, r, err)
		}
	}
	// 结算使用已锁定的值，配置变更和时间跨界不会修改旧请求的价格。
	locked, _ := p.resolve(parse("2026-10-24T22:00:00+08:00"), 1000, 4000, 10000)
	reserved, err := price(1000, 1000, locked.input, locked.output, locked.multiplier)
	if err != nil {
		t.Fatal(err)
	}
	p.peakInput = 999999
	p.peakMultiplier = 20000
	if actual, err := price(1000, 1000, locked.input, locked.output, locked.multiplier); err != nil || actual != reserved || actual != 15000 {
		t.Fatalf("locked charge changed: %d", actual)
	}
}

func TestInvalidScheduledPricingFails(t *testing.T) {
	valid := modelPricing{timeZone: "Asia/Shanghai", peakStart: sql.NullString{String: "09:00", Valid: true}, peakEnd: sql.NullString{String: "18:00", Valid: true}, peakMultiplier: 10000, limitedStart: sql.NullInt64{Int64: 1, Valid: true}, limitedEnd: sql.NullInt64{Int64: 2, Valid: true}, limitedMultiplier: 10000}
	mutations := []func(*modelPricing){func(p *modelPricing) { p.timeZone = "invalid/zone" }, func(p *modelPricing) { p.peakEnabled = true; p.peakStart.String = "25:00" }, func(p *modelPricing) { p.peakEnabled = true; p.peakEnd = p.peakStart }, func(p *modelPricing) { p.limitedEnabled = true; p.limitedEnd = p.limitedStart }, func(p *modelPricing) { p.limitedEnabled = true; p.limitedStart.Valid = false }, func(p *modelPricing) { p.limitedEnabled = true; p.limitedMultiplier = 0 }}
	for i, mutate := range mutations {
		p := valid
		mutate(&p)
		if _, err := p.resolve(time.Now(), 1, 1, 10000); err == nil {
			t.Fatalf("invalid pricing %d accepted", i)
		}
	}
}
