package bot

import (
	"regexp"
	"strings"

	"qqbot/internal/onebot"
)

// cqCodeRe 匹配 CQ 码段，如 [CQ:at,qq=123]、[CQ:image,file=...]
// 关键词匹配必须剔除 CQ 码，否则 @ 消息里的 QQ 号会被误判为文本内容
var cqCodeRe = regexp.MustCompile(`\[CQ:[^\]]*\]`)

// extractText 提取消息纯文本（剔除 CQ 码，防止 @ 对象 QQ 号参与关键词匹配）。
func extractText(m onebot.GroupMessage) string {
	if m.RawMessage != "" {
		return cqCodeRe.ReplaceAllString(m.RawMessage, "")
	}
	var sb strings.Builder
	for _, seg := range m.Message {
		if seg.Type == "text" {
			if s, ok := seg.Data["text"].(string); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}
