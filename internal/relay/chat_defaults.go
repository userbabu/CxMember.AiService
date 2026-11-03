package relay

import "errors"

func reasoningRank(effort string) int {
	for rank, value := range reasoningTiers {
		if value == effort {
			return rank
		}
	}
	return -1
}

// 默认档位优先选不超过目标的最高可用级别；目标低于全部档位时选最低可用级别。
func defaultReasoningEffort(requested string, allowed []string) (string, error) {
	target := reasoningRank(requested)
	if target < 0 {
		return "", errors.New("invalid default reasoning effort")
	}
	if len(allowed) == 0 {
		return "none", nil
	}
	best, lowest := -1, len(reasoningTiers)
	for _, effort := range allowed {
		rank := reasoningRank(effort)
		if rank < 0 {
			return "", errors.New("invalid model reasoning effort")
		}
		if rank < lowest {
			lowest = rank
		}
		if rank <= target && rank > best {
			best = rank
		}
	}
	if best < 0 {
		best = lowest
	}
	return reasoningTiers[best], nil
}
