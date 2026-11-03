package relay

import (
	"context"
	"errors"
	"time"
)

func (s *Server) generationCatalog(ctx context.Context, product string, now time.Time) (map[string]any, error) {
	rows, e := s.store.db.QueryContext(ctx, `SELECT m.model_id,m.model_type,m.generation_api,m.generation_size,p.generation_price_micro_per_unit,p.peak_generation_price_micro_per_unit,p.limited_generation_price_micro_per_unit,p.time_zone,p.peak_enabled,p.peak_start,p.peak_end,p.limited_enabled,p.limited_start_unix,p.limited_end_unix FROM ai_model m JOIN ai_model_pricing p ON p.model_id=m.model_id WHERE m.enabled=1 AND m.model_type IN ('image','video') AND (m.product_scope='' OR m.product_scope=?)`, product)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var id, kind, api, size string
		var standard int64
		var p modelPricing
		if e = rows.Scan(&id, &kind, &api, &size, &standard, &p.peakInput, &p.limitedInput, &p.timeZone, &p.peakEnabled, &p.peakStart, &p.peakEnd, &p.limitedEnabled, &p.limitedStart, &p.limitedEnd); e != nil {
			return nil, e
		}
		if !validGenerationAPI(kind, api) || size == "" {
			return nil, errors.New("invalid generation model configuration")
		}
		p.peakMultiplier, p.limitedMultiplier = 10000, 10000
		rate, e := p.resolve(now, standard, 0, 10000)
		if e != nil {
			return nil, e
		}
		if _, e = generationAmount(rate.input, 1); e != nil {
			return nil, e
		}
		unit := "image"
		if kind == "video" {
			unit = "second"
		}
		out[id] = map[string]any{"api": api, "size": size, "unit": unit, "unit_price_credit": formatCredit(rate.input), "price_kind": rate.kind}
	}
	return out, rows.Err()
}
