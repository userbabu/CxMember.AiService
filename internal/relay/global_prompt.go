package relay

import "strings"

// 全局规则独占 system 层级；客户端补充要求保留内容，但不能与全局规则同级。
func applyGlobalPrompt(messages []map[string]any, prompt string) []map[string]any {
	if strings.TrimSpace(prompt) == "" {
		return messages
	}
	result := make([]map[string]any, 0, len(messages)+1)
	result = append(result, map[string]any{"role": "system", "content": prompt})
	for _, message := range messages {
		if message["role"] == "system" || message["role"] == "developer" {
			copy := make(map[string]any, len(message))
			for key, value := range message {
				copy[key] = value
			}
			copy["role"] = "user"
			result = append(result, copy)
		} else {
			result = append(result, message)
		}
	}
	return result
}
