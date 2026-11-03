package relay

// 所有管理端与客户端均以二进制 token 档位显示，1M = 1,048,576。
var modelContextTiers = [...]int{4096, 8192, 16384, 32768, 65536, 131072, 262144, 524288, 1048576}

func validContextLimit(limit int) bool {
	for _, tier := range modelContextTiers {
		if limit == tier {
			return true
		}
	}
	return false
}
func contextTiers(limit int) []int {
	tiers := []int{}
	for _, tier := range modelContextTiers {
		if tier <= limit {
			tiers = append(tiers, tier)
		}
	}
	return tiers
}
