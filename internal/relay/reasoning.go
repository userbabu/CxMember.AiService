package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var reasoningTiers = [...]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// 白名单独立于总开关保存；关停时对客户公布空列表，不能因上游默认开启而透传思考。
func reasoningPolicy(raw, format string, enabled bool) ([]string, error) {
	if format != "reasoning_effort" && format != "deepseek" {
		return nil, errors.New("invalid reasoning format")
	}
	var tiers []string
	if err := json.Unmarshal([]byte(raw), &tiers); err != nil || tiers == nil {
		return nil, errors.New("invalid reasoning tiers")
	}
	seen := map[string]bool{}
	for _, tier := range tiers {
		valid := false
		for _, supported := range reasoningTiers {
			if tier == supported {
				valid = true
				break
			}
		}
		if !valid || seen[tier] {
			return nil, errors.New("invalid reasoning tier")
		}
		seen[tier] = true
	}
	if !enabled {
		return []string{}, nil
	}
	if len(tiers) == 0 {
		return nil, errors.New("enabled reasoning requires tiers")
	}
	return tiers, nil
}

// 统一校验两种输入写法，禁止利用 thinking 绕开档位或发送互相矛盾的配置。
func selectReasoning(req chatRequest, allowed []string) (string, error) {
	effort := req.ReasoningEffort
	mode := ""
	if len(req.Thinking) != 0 && string(req.Thinking) != "null" {
		var toggle struct {
			Type string `json:"type"`
		}
		decoder := json.NewDecoder(bytes.NewReader(req.Thinking))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&toggle); err != nil {
			return "", errors.New("invalid thinking object")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return "", errors.New("invalid thinking object")
		}
		if toggle.Type != "enabled" && toggle.Type != "disabled" {
			return "", errors.New("invalid thinking mode")
		}
		mode = toggle.Type
	}
	if effort == "" {
		effort = "none"
		if len(allowed) > 0 {
			effort = allowed[0]
		}
		if mode == "disabled" {
			effort = "none"
		}
		if mode == "enabled" && effort == "none" {
			for _, tier := range allowed {
				if tier != "none" {
					effort = tier
					break
				}
			}
		}
	}
	if mode == "enabled" && effort == "none" || mode == "disabled" && effort != "none" {
		return "", errors.New("conflicting reasoning settings")
	}
	if len(allowed) == 0 {
		if effort == "none" {
			return effort, nil
		}
	} else {
		for _, tier := range allowed {
			if effort == tier {
				return effort, nil
			}
		}
	}
	return "", errors.New("reasoning tier is not enabled")
}

func applyReasoning(body map[string]any, format, effort string) {
	if format == "deepseek" {
		mode := "enabled"
		if effort == "none" {
			mode = "disabled"
		} else {
			body["reasoning_effort"] = effort
		}
		body["thinking"] = map[string]string{"type": mode}
	} else {
		body["reasoning_effort"] = effort
	}
}
