package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

const imageTokenEstimate int64 = 8192

// 图片不按 base64 文本估算；禁止远程 URL，避免把内网地址或凭据交给供应商抓取。
func chatInputEstimate(messages []map[string]any, tools, toolChoice json.RawMessage, allowImages bool, policy imageInputPolicy) (int64, error) {
	if policy.tokensPerImage <= 0 || !validModelImageLimit(policy.maxFileBytes) || !validModelImageCount(policy.maxImages) {
		return 0, errors.New("invalid model image token policy")
	}
	clean := make([]map[string]any, 0, len(messages))
	imageCount, decodedBytes := 0, 0
	for _, message := range messages {
		copy := make(map[string]any, len(message))
		for key, value := range message {
			copy[key] = value
		}
		parts, ok := message["content"].([]any)
		if !ok {
			clean = append(clean, copy)
			continue
		}
		cleanParts := make([]any, 0, len(parts))
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				return 0, errors.New("invalid message content part")
			}
			if part["type"] == "text" {
				cleanParts = append(cleanParts, part)
				continue
			}
			if part["type"] != "image_url" {
				return 0, errors.New("unsupported message content type")
			}
			if !allowImages {
				return 0, errors.New("image input is disabled for this model")
			}
			if message["role"] != "user" {
				return 0, errors.New("images are only allowed in user messages")
			}
			value, ok := part["image_url"].(map[string]any)
			if !ok {
				return 0, errors.New("invalid image input")
			}
			url, ok := value["url"].(string)
			if !ok {
				return 0, errors.New("invalid image input")
			}
			format, payload := "", ""
			for mime, name := range map[string]string{"image/png": "png", "image/jpeg": "jpeg"} {
				prefix := "data:" + mime + ";base64,"
				if strings.HasPrefix(url, prefix) {
					format, payload = name, strings.TrimPrefix(url, prefix)
					break
				}
			}
			if format == "" {
				return 0, errors.New("images must be PNG or JPEG data URLs")
			}
			if len(payload) > base64.StdEncoding.EncodedLen(policy.maxFileBytes) {
				return 0, errors.New("image exceeds model file size limit")
			}
			data, err := base64.StdEncoding.Strict().DecodeString(payload)
			if err != nil || len(data) == 0 {
				return 0, errors.New("invalid image encoding")
			}
			if len(data) > policy.maxFileBytes {
				return 0, errors.New("image exceeds model file size limit")
			}
			decodedBytes += len(data)
			imageCount++
			if imageCount > policy.maxImages {
				return 0, errors.New("image count exceeds model request limit")
			}
			if decodedBytes > maxImageTotalBytes {
				return 0, errors.New("image request exceeds total limit")
			}
			cfg, actualFormat, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || actualFormat != format || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 {
				return 0, errors.New("images must be valid PNG or JPEG up to 2048 pixels per side")
			}
			if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
				return 0, errors.New("invalid image data")
			}
			if detail, exists := value["detail"]; exists && detail != "auto" && detail != "low" && detail != "high" && !(policy.allowOriginal && detail == "original") {
				return 0, errors.New("invalid image detail")
			}
			cleanParts = append(cleanParts, map[string]any{"type": "image_url"})
		}
		copy["content"] = cleanParts
		clean = append(clean, copy)
	}
	context, err := json.Marshal(map[string]any{"messages": clean, "tools": tools, "tool_choice": toolChoice})
	if err != nil {
		return 0, err
	}
	return int64((len(context)+2)/3+len(messages)*8) + int64(imageCount)*policy.tokensPerImage, nil
}

// 按完整上下文选择价格，工具续轮与带历史图片的追问仍使用含图请求价。
func hasImageInput(messages []map[string]any) bool {
	for _, message := range messages {
		if parts, ok := message["content"].([]any); ok {
			for _, raw := range parts {
				if part, ok := raw.(map[string]any); ok && part["type"] == "image_url" {
					return true
				}
			}
		}
	}
	return false
}
