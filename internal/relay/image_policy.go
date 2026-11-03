package relay

import (
	"net/url"
	"strings"
)

const maxImageFileBytes = 32 << 20
const maxImageTotalBytes = 32 << 20

func validModelImageLimit(size int) bool {
	return size >= 1024 && size <= maxImageFileBytes && size%1024 == 0
}

func validModelImageCount(count int) bool { return count >= 1 && count <= 32 }

type imageInputPolicy struct {
	maxImages      int
	maxFileBytes   int
	tokensPerImage int64
	allowOriginal  bool
}

// 仅对官方端点和文档明确支持的上游模型使用1024上限，不按展示名推断第三方代理能力。
func modelImageInputPolicy(baseURL, upstream string) imageInputPolicy {
	policy := imageInputPolicy{tokensPerImage: imageTokenEstimate, maxFileBytes: 4 << 20, maxImages: 4}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "api.deepseek.com") ||
		(u.Port() != "" && u.Port() != "443") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return policy
	}
	switch strings.TrimRight(u.Path, "/") {
	case "", "/v1", "/chat/completions", "/v1/chat/completions":
	default:
		return policy
	}
	if upstream == "deepseek-flash" || upstream == "deepseek-v4-flash-vision-exp" {
		policy.tokensPerImage = 1024
		policy.allowOriginal = true
	}
	return policy
}
