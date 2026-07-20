package parse

import "strings"

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
	case "SKILL_NAME_MISMATCH":
		return "重新上传的 Skill 与当前 Skill 不一致"
	case "DUPLICATE_NAME":
		return "当前 Space 下已存在同名 Skill"
	case "PARSE_RETRY_EXHAUSTED":
		return "解析任务多次超时，请重新上传"
	default:
		return "解析失败"
	}
}

func publicParseErrorMessageWithDetail(errorCode, detail string) string {
	if errorCode == "SKILL_NAME_MISMATCH" {
		detail = strings.TrimSpace(detail)
		if detail != "" {
			return detail
		}
	}
	return publicParseErrorMessage(errorCode)
}
