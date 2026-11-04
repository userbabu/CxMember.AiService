package relay

import "testing"

func TestDefaultChatModelIDPicksFirstCatalogDefault(t *testing.T) {
	// 目录顺序即服务端 ORDER BY 结果；首个标记为默认的文本模型生效。
	models := []map[string]any{
		{"id": " first-default ", "model_type": "text", "is_default": true},
		{"id": "second-default", "model_type": "text", "is_default": true},
	}
	if got := defaultChatModelID(models); got != " first-default " {
		t.Fatalf("defaultChatModelID=%q want first catalog default", got)
	}
	if got := defaultChatModelID([]map[string]any{{"id": "plain", "model_type": "text", "is_default": false}}); got != "" {
		t.Fatalf("unexpected default %q for unmarked catalog", got)
	}
}

func TestDefaultChatModelIDIgnoresNonTextModels(t *testing.T) {
	// 图片/视频模型的默认标记不得进入聊天默认模型。
	for _, modelType := range []string{"image", "video"} {
		models := []map[string]any{{"id": "media", "model_type": modelType, "is_default": true}}
		if got := defaultChatModelID(models); got != "" {
			t.Fatalf("%s default leaked into chat: %q", modelType, got)
		}
	}
}
