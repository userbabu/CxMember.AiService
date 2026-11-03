package relay

import (
	"context"
	"database/sql"
	"errors"
	"time"
	_ "time/tzdata"
)

type modelPricing struct {
	visionInput, visionCacheInput, visionOutput                      int64
	peakVisionInput, peakVisionCacheInput, peakVisionOutput          int64
	limitedVisionInput, limitedVisionCacheInput, limitedVisionOutput int64
	timeZone                                                         string
	cacheInput                                                       int64
	peakEnabled                                                      bool
	peakStart, peakEnd                                               sql.NullString
	peakInput, peakOutput                                            int64
	peakCacheInput                                                   int64
	peakMultiplier                                                   uint64
	limitedEnabled                                                   bool
	limitedStart, limitedEnd                                         sql.NullInt64
	limitedInput, limitedOutput                                      int64
	limitedCacheInput                                                int64
	limitedMultiplier                                                uint64
}

type billingRate struct {
	generationKind            string
	generationPrice           int64
	generationUnits           int
	vision                    bool
	kind, timeZone            string
	lockedAt                  time.Time
	input, cacheInput, output int64
	cachedTokens              int64
	multiplier                uint64
}

// 含图请求整次采用多模态token价，不把已计入prompt_tokens的图片再重复计费。
func (p modelPricing) resolveForInput(now time.Time, input, output int64, multiplier uint64, vision bool) (billingRate, error) {
	if !vision {
		return p.resolve(now, input, output, multiplier)
	}
	p.cacheInput = p.visionCacheInput
	p.peakInput, p.peakCacheInput, p.peakOutput = p.peakVisionInput, p.peakVisionCacheInput, p.peakVisionOutput
	p.limitedInput, p.limitedCacheInput, p.limitedOutput = p.limitedVisionInput, p.limitedVisionCacheInput, p.limitedVisionOutput
	rate, err := p.resolve(now, p.visionInput, p.visionOutput, multiplier)
	rate.vision = true
	return rate, err
}
func (r billingRate) inputKind() string {
	if r.generationKind != "" {
		return r.generationKind + "_generation"
	}
	if r.vision {
		return "vision"
	}
	return "text"
}

// 请求在进入限流队列前锁价，跨时段和后台改价都不改变本次预扣及结算价格。
func (p modelPricing) resolve(now time.Time, input, output int64, multiplier uint64) (billingRate, error) {
	rate := billingRate{kind: "standard", timeZone: p.timeZone, lockedAt: now.UTC(), input: input, cacheInput: p.cacheInput, output: output, multiplier: multiplier}
	location, err := time.LoadLocation(p.timeZone)
	if err != nil {
		return rate, errors.New("invalid pricing time zone")
	}
	var start, end int
	if p.peakEnabled {
		startTime, startErr := time.Parse("15:04", p.peakStart.String)
		endTime, endErr := time.Parse("15:04", p.peakEnd.String)
		if !p.peakStart.Valid || !p.peakEnd.Valid || startErr != nil || endErr != nil {
			return rate, errors.New("invalid peak interval")
		}
		start, end = startTime.Hour()*60+startTime.Minute(), endTime.Hour()*60+endTime.Minute()
		if start == end || p.peakMultiplier == 0 || p.peakInput < 0 || p.peakCacheInput < 0 || p.peakOutput < 0 {
			return rate, errors.New("invalid peak pricing")
		}
	}
	if p.limitedEnabled && (!p.limitedStart.Valid || !p.limitedEnd.Valid || p.limitedStart.Int64 >= p.limitedEnd.Int64 || p.limitedMultiplier == 0 || p.limitedInput < 0 || p.limitedCacheInput < 0 || p.limitedOutput < 0) {
		return rate, errors.New("invalid limited pricing")
	}
	if input < 0 || p.cacheInput < 0 || output < 0 || multiplier == 0 {
		return rate, errors.New("invalid standard pricing")
	}
	// 活动起点包含、终点不包含；活动覆盖同一时刻的高峰配置。
	if p.limitedEnabled && now.Unix() >= p.limitedStart.Int64 && now.Unix() < p.limitedEnd.Int64 {
		rate.kind, rate.input, rate.cacheInput, rate.output, rate.multiplier = "limited", p.limitedInput, p.limitedCacheInput, p.limitedOutput, p.limitedMultiplier
		return rate, nil
	}
	local := now.In(location)
	minute := local.Hour()*60 + local.Minute()
	if p.peakEnabled && (start < end && minute >= start && minute < end || start > end && (minute >= start || minute < end)) {
		rate.kind, rate.input, rate.cacheInput, rate.output, rate.multiplier = "peak", p.peakInput, p.peakCacheInput, p.peakOutput, p.peakMultiplier
	}
	return rate, nil
}

// 公开消耗倍率仅由最终售价推导，不包含供应商成本或凭据。
func (s *Server) publicModelPrices(ctx context.Context, product string) ([]map[string]any, error) {
	defaults, err := s.store.ederServiceConfig(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT m.model_id,m.display_name,m.model_type,m.allow_images,m.max_image_bytes,m.max_images_per_request,m.max_context_tokens,m.allow_thinking,m.reasoning_efforts,m.reasoning_format,m.input_price_micro_per_1k,m.cache_input_price_micro_per_1k,m.output_price_micro_per_1k,m.multiplier_bps,p.time_zone,p.peak_enabled,p.peak_start,p.peak_end,p.peak_input_price_micro_per_1k,p.peak_cache_input_price_micro_per_1k,p.peak_output_price_micro_per_1k,p.peak_multiplier_bps,p.limited_enabled,p.limited_start_unix,p.limited_end_unix,p.limited_input_price_micro_per_1k,p.limited_cache_input_price_micro_per_1k,p.limited_output_price_micro_per_1k,p.limited_multiplier_bps,p.vision_input_price_micro_per_1k,p.vision_cache_input_price_micro_per_1k,p.vision_output_price_micro_per_1k,p.peak_vision_input_price_micro_per_1k,p.peak_vision_cache_input_price_micro_per_1k,p.peak_vision_output_price_micro_per_1k,p.limited_vision_input_price_micro_per_1k,p.limited_vision_cache_input_price_micro_per_1k,p.limited_vision_output_price_micro_per_1k FROM ai_model m JOIN ai_model_pricing p ON p.model_id=m.model_id JOIN ai_provider v ON v.provider_id=m.provider_id AND v.enabled=1 JOIN ai_provider_credential c ON c.credential_id=m.credential_id AND c.enabled=1 WHERE m.enabled=1 AND (m.product_scope='' OR m.product_scope=?) ORDER BY m.display_name`, product)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	data := []map[string]any{}
	now := time.Now()
	generationCatalog, err := s.generationCatalog(ctx, product, now)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, name, modelType string
		var maxContext, maxImageBytes, maxImages int
		var allowThinking, allowImages bool
		var effortsJSON, reasoningFormat string
		var input, cacheInput, output int64
		var multiplier uint64
		var p modelPricing
		if err := rows.Scan(&id, &name, &modelType, &allowImages, &maxImageBytes, &maxImages, &maxContext, &allowThinking, &effortsJSON, &reasoningFormat, &input, &cacheInput, &output, &multiplier, &p.timeZone, &p.peakEnabled, &p.peakStart, &p.peakEnd, &p.peakInput, &p.peakCacheInput, &p.peakOutput, &p.peakMultiplier, &p.limitedEnabled, &p.limitedStart, &p.limitedEnd, &p.limitedInput, &p.limitedCacheInput, &p.limitedOutput, &p.limitedMultiplier, &p.visionInput, &p.visionCacheInput, &p.visionOutput, &p.peakVisionInput, &p.peakVisionCacheInput, &p.peakVisionOutput, &p.limitedVisionInput, &p.limitedVisionCacheInput, &p.limitedVisionOutput); err != nil {
			return nil, err
		}
		if !validModelImageCount(maxImages) {
			return nil, errors.New("invalid model image count limit")
		}
		if !validModelImageLimit(maxImageBytes) {
			return nil, errors.New("invalid model image size limit")
		}
		if !validModelType(modelType) {
			return nil, errors.New("invalid model type")
		}
		if !validContextLimit(maxContext) {
			return nil, errors.New("invalid model context limit")
		}
		efforts, err := reasoningPolicy(effortsJSON, reasoningFormat, allowThinking)
		if err != nil {
			return nil, err
		}
		defaultEffort, err := defaultReasoningEffort(defaults.DefaultReasoningEffort, efforts)
		if err != nil {
			return nil, err
		}
		defaultContext := defaults.DefaultContextTokens
		if defaultContext > maxContext {
			defaultContext = maxContext
		}
		p.cacheInput = cacheInput
		rate, err := p.resolve(now, input, output, multiplier)
		if err != nil {
			return nil, err
		}
		visionRate, err := p.resolveForInput(now, input, output, multiplier, true)
		if err != nil {
			return nil, err
		}
		visionPublic, err := visionRate.publicConsumption()
		if err != nil {
			return nil, err
		}
		public, err := rate.publicConsumption()
		if err != nil {
			return nil, err
		}
		data = append(data, map[string]any{"id": id, "object": "model", "created": 0, "owned_by": "cx", "name": name, "model_type": modelType, "allow_images": allowImages, "max_image_bytes": maxImageBytes, "max_images_per_request": maxImages, "max_context_tokens": maxContext, "context_tiers": contextTiers(maxContext), "default_context_tokens": defaultContext, "default_reasoning_effort": defaultEffort, "reasoning_efforts": efforts, "pricing": public, "vision_pricing": visionPublic, "generation": generationCatalog[id]})
	}
	return data, rows.Err()
}

func (r billingRate) publicConsumption() (map[string]any, error) {
	input, err := price(1000, 0, r.input, r.output, r.multiplier)
	if err != nil {
		return nil, err
	}
	output, err := price(0, 1000, r.input, r.output, r.multiplier)
	if err != nil {
		return nil, err
	}
	cache, err := price(1000, 0, r.cacheInput, 0, r.multiplier)
	if err != nil {
		return nil, err
	}
	return map[string]any{"kind": r.kind, "unit": "credit", "input_credit_per_1k": formatCredit(input), "cache_input_credit_per_1k": formatCredit(cache), "output_credit_per_1k": formatCredit(output)}, nil
}
