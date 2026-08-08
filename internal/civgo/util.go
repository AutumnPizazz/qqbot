package civgo

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// ---- 文本工具（自旧 chunk.go / index.go 迁入，向量检索删除后保留） ----

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

// ---- 文档扫描（自旧 chunk.go / index.go 迁入） ----

// SupportedExt 判断文件扩展名是否支持（.md / .txt，大小写不敏感）。
func SupportedExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".txt":
		return true
	}
	return false
}

// isIndexFile 判断是否为索引类文档（文件名含「索引」或 index）。
// 这类文件描述的是文档结构而非游戏内容：工具描述引导 AI 优先阅读。
func isIndexFile(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	return strings.Contains(base, "索引") || strings.Contains(base, "index")
}

// scanDocs 递归扫描目录下的支持类型文件，返回相对路径列表（排除隐藏文件）。
func scanDocs(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !SupportedExt(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil // 目录尚未存在（首次同步前）
	}
	sort.Strings(out)
	return out, err
}

// sha256Hex 计算内容哈希（docmap 增量重建的文件指纹）。
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// ---- 分词（自旧 index.go 迁入，历史召回关键词过滤用） ----

// tokenize 中文混合分词：字母数字串整体成词；连续 CJK 汉字按二元切分。
// 类型切换（字母数字 ↔ CJK ↔ 其他）时冲刷当前缓冲区。
func tokenize(s string) []string {
	lower := strings.ToLower(s)
	var tokens []string
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) > 0 {
			tokens = append(tokens, string(word))
			word = nil
		}
	}
	flushCJK := func() {
		if len(cjk) > 0 {
			for i := 0; i+1 < len(cjk); i++ {
				tokens = append(tokens, string(cjk[i:i+2]))
			}
			cjk = nil
		}
	}
	for _, r := range lower {
		switch {
		case isCJK(r):
			if len(word) > 0 {
				flushWord()
			}
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if len(cjk) > 0 {
				flushCJK()
			}
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return tokens
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

// truncateStr 按字节截断字符串用于日志/错误信息。
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
