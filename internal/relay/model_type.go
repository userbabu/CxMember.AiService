package relay

const (
	modelTypeText  = "text"
	modelTypeImage = "image"
	modelTypeVideo = "video"
)

// 固定模型分类必须与数据库 ENUM 和公开接口值一致。
func validModelType(value string) bool {
	switch value {
	case modelTypeText, modelTypeImage, modelTypeVideo:
		return true
	default:
		return false
	}
}
