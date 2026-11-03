package relay

import (
	"encoding/json"
	"errors"
)

// 上游只允许返回标准补全字段；错误正文及供应商私有诊断字段不得透传。
func safeCompletionFrame(data []byte) ([]byte, int64, int64, int64, error) {
	var frame map[string]json.RawMessage
	if json.Unmarshal(data, &frame) != nil || frame == nil {
		return nil, 0, 0, 0, errors.New("invalid upstream completion JSON")
	}
	if value, exists := frame["error"]; exists && string(value) != "null" {
		return nil, 0, 0, 0, errors.New("upstream returned an error frame")
	}
	var choices []json.RawMessage
	if value, exists := frame["choices"]; !exists || json.Unmarshal(value, &choices) != nil || choices == nil {
		return nil, 0, 0, 0, errors.New("invalid upstream completion choices")
	}
	var usage struct {
		Prompt           int64 `json:"prompt_tokens"`
		Completion       int64 `json:"completion_tokens"`
		CacheHit         int64 `json:"prompt_cache_hit_tokens"`
		PromptTokenParts struct {
			Cached int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	}
	var usageFields map[string]json.RawMessage
	if value, exists := frame["usage"]; exists && string(value) != "null" {
		if json.Unmarshal(value, &usage) != nil || json.Unmarshal(value, &usageFields) != nil || usage.Prompt < 0 || usage.Completion < 0 || usage.CacheHit < 0 || usage.PromptTokenParts.Cached < 0 {
			return nil, 0, 0, 0, errors.New("invalid upstream usage")
		}
	}
	directHitPresent := len(usageFields["prompt_cache_hit_tokens"]) > 0 && string(usageFields["prompt_cache_hit_tokens"]) != "null"
	var tokenDetails map[string]json.RawMessage
	if details := usageFields["prompt_tokens_details"]; len(details) > 0 && string(details) != "null" && json.Unmarshal(details, &tokenDetails) != nil {
		return nil, 0, 0, 0, errors.New("invalid upstream cache usage")
	}
	nestedHitPresent := len(tokenDetails["cached_tokens"]) > 0 && string(tokenDetails["cached_tokens"]) != "null"
	if directHitPresent && nestedHitPresent && usage.CacheHit != usage.PromptTokenParts.Cached {
		return nil, 0, 0, 0, errors.New("conflicting upstream cache usage")
	}
	if !directHitPresent {
		usage.CacheHit = usage.PromptTokenParts.Cached
	}
	if usage.CacheHit > usage.Prompt {
		return nil, 0, 0, 0, errors.New("cached usage exceeds prompt usage")
	}
	allowed := map[string]json.RawMessage{}
	for _, key := range []string{"id", "object", "created", "model", "choices", "usage", "system_fingerprint", "service_tier"} {
		if value, exists := frame[key]; exists {
			allowed[key] = value
		}
	}
	cleaned, err := json.Marshal(allowed)
	return cleaned, usage.Prompt, usage.Completion, usage.CacheHit, err
}
