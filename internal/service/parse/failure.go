package parse

func publicParseErrorMessage(errorCode string) string {
	switch errorCode {
	case "INTERNAL_ERROR":
		return "解析任务执行失败，请稍后重试"
	case "INVALID_ZIP":
		return "上传文件不是有效的 ZIP 压缩包"
	case "FILE_TOO_LARGE":
		return "上传文件超过大小限制"
	case "SKILL_MD_TOO_LARGE":
		return "SKILL.md 超过大小限制"
	case "SKILL_MD_NOT_FOUND":
		return "压缩包中缺少 SKILL.md"
	case "ZIP_SLIP_DETECTED":
		return "压缩包包含不安全的文件路径"
	case "INVALID_SKILL_MD":
		return "SKILL.md 内容不符合要求"
	case "DUPLICATE_NAME":
		return "当前 Space 下已存在同名 Skill"
	default:
		return "解析失败"
	}
}
