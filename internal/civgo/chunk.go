package civgo

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Chunk 文档分块（检索与上下文注入的最小单元）。
type Chunk struct {
	File    string `json:"file"`    // 相对 docs_path 的路径，如 "units/archer.md"
	Heading string `json:"heading"` // 所属最近标题（无则空）
	Index   int    `json:"index"`   // 文件内块序号（从 0）
	Text    string `json:"text"`
}

// headingRe 匹配 markdown 标题行（# 至 ######）。
var headingRe = regexp.MustCompile(`^#{1,6}\s+`)

// SupportedExt 判断文件扩展名是否支持分块（.md / .txt，大小写不敏感）。
func SupportedExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".txt":
		return true
	}
	return false
}

// ChunkFile 切分单个文件。返回 nil 表示跳过（非支持类型/内容为空）。
// 算法（按行流式单遍）：
//   - 标题行开启语义块：封口当前块、更新 heading，标题本身作为新块首行；
//   - 普通行整行追加（不跨行硬切，避免切断表格/代码）；块 rune 数 ≥ chunk_size 即封口；
//   - 结尾封口；纯空白块过滤。
//
// chunkOverlap 为预留参数（首版不实现字符级重叠，整行切块已避免大部分语义断裂）。
func ChunkFile(relPath, content string, chunkSize, chunkOverlap int) []Chunk {
	if !SupportedExt(relPath) {
		return nil
	}
	if chunkSize < minChunkSize {
		chunkSize = minChunkSize
	}
	_ = chunkOverlap // 预留
	var chunks []Chunk
	var cur strings.Builder
	heading := ""

	flush := func() {
		if cur.Len() == 0 {
			return
		}
		text := strings.TrimSpace(cur.String())
		if text == "" {
			cur.Reset()
			return
		}
		chunks = append(chunks, Chunk{
			File:    relPath,
			Heading: heading,
			Index:   len(chunks),
			Text:    text,
		})
		cur.Reset()
	}

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		if headingRe.MatchString(line) {
			flush()
			heading = strings.TrimSpace(headingRe.ReplaceAllString(line, ""))
			cur.WriteString(line)
			cur.WriteString("\n")
		} else {
			cur.WriteString(line)
			cur.WriteString("\n")
		}
		if runeLen(cur.String()) >= chunkSize {
			flush()
		}
	}
	flush()
	return chunks
}

// runeLen 字符串的 rune 数量。
func runeLen(s string) int {
	return len([]rune(s))
}

// truncateRunes 按 rune 截断到 max 字符（超出追加省略号）。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// trimLeadingNonCJK 去掉问题文本前导的 @昵称残留与空白标点。
// CQ 码剔除后 at 通常无残留，此处兜底处理「@昵称 问题」形式。
func trimLeadingNonCJK(s string) string {
	// 剔除 @ 打头的昵称残留：@昵称 直到第一个空白
	if strings.HasPrefix(s, "@") {
		if i := strings.IndexAny(s, " \t\n"); i > 0 {
			s = s[i:]
		} else {
			return "" // 只有 @昵称无正文
		}
	}
	return strings.TrimLeftFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	})
}
