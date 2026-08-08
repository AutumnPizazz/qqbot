package civgo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// listDocsMaxLines 单次 list_docs 输出行数上限（超出提示下钻）。
const listDocsMaxLines = 60

// ToolExecutor 工具执行器：绑定文档地图与文档目录，agent 循环内串行执行。
// 工具集：list_docs / get_doc_outline / read_doc（recall_history 由 history 模块注册）。
type ToolExecutor struct {
	docmap  *DocmapStore
	docsDir string
	cfg     func() *Config // 热重载快照
}

// NewToolExecutor 创建工具执行器。
func NewToolExecutor(docmap *DocmapStore, docsDir string, cfg func() *Config) *ToolExecutor {
	return &ToolExecutor{docmap: docmap, docsDir: docsDir, cfg: cfg}
}

// ContextBudget 上下文预算：read_doc 内容计入 readUsed；list/outline 计入 lightUsed。
// 超限后对应工具拒绝执行，强制 AI 基于已有信息作答。
type ContextBudget struct {
	readUsed  int
	readMax   int
	lightUsed int
	lightMax  int
}

// newContextBudget 按 agent 配置创建预算（light 额度为 read 的 20%）。
func newContextBudget(cfg AgentConfig) *ContextBudget {
	return &ContextBudget{
		readMax:  cfg.MaxContextChars,
		lightMax: cfg.MaxContextChars / 5,
	}
}

// Definitions 返回工具定义列表（Responses API tools 数组）。
func (e *ToolExecutor) Definitions() []Tool {
	return []Tool{
		{
			Name: "list_docs",
			Description: "列出文档知识库的目录内容（文件名/行数/字节数）。回答游戏问题时通常先调用本工具了解有哪些文档，" +
				"再进一步阅读。⭐ 标记的为索引/阅读顺序类文档，了解文档结构时建议优先阅读。" +
				"可传 path 参数下钻子目录。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "子目录相对路径，省略则列出根目录"},
				},
				"additionalProperties": false,
			},
		},
		{
			Name: "get_doc_outline",
			Description: "查看某个文档的标题大纲（标题+行号），用于定位需要阅读的段落。" +
				"path 为 list_docs 返回的文档相对路径。查看大纲后再用 read_doc 按行号精准阅读，避免整篇读取。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "文档相对路径（必填）"},
				},
				"required":             []string{"path"},
				"additionalProperties": false,
			},
		},
		{
			Name: "read_doc",
			Description: "按行号范围读取文档内容（默认从第 1 行读起，可传 start_line 与 max_lines 翻页）。" +
				"path 为文档相对路径。返回带行号的内容，可多次调用翻页阅读。" +
				"注意：上下文空间有限，只读与问题相关的段落，不要整篇读取。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":       map[string]any{"type": "string", "description": "文档相对路径（必填）"},
					"start_line": map[string]any{"type": "integer", "description": "起始行号（1 起，默认 1）"},
					"max_lines":  map[string]any{"type": "integer", "description": "本次最多读取行数（默认 200）"},
				},
				"required":             []string{"path"},
				"additionalProperties": false,
			},
		},
	}
}

// Execute 执行一次工具调用，返回给 AI 的结果文本（任何情况不 panic）。
func (e *ToolExecutor) Execute(name, argsJSON string, budget *ContextBudget) (string, error) {
	args := map[string]any{}
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			// 坏 JSON：按空参数继续（工具名有效即可），日志记录
			args = map[string]any{}
		}
	}
	switch name {
	case "list_docs":
		return e.listDocs(args, budget), nil
	case "get_doc_outline":
		return e.getDocOutline(args, budget), nil
	case "read_doc":
		return e.readDoc(args, budget)
	default:
		return "", fmt.Errorf("未知工具: %s", name)
	}
}

// ---- 工具实现 ----

// argString 读取字符串参数。
func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// argInt 读取整数参数；非法/缺失返回 def。
func argInt(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

// docRel 归一化文档相对路径（去前导 ./ 与尾部 /），空返回 false。
func docRel(s string) (string, bool) {
	s = filepath.ToSlash(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "./")
	if s == "" || strings.HasPrefix(s, "/") || strings.Contains(s, "..") {
		return "", false
	}
	return s, true
}

// safeJoin 校验 rel 不逃逸 base 后拼接绝对路径（跨平台：/ 与 \ 开头均拒绝）。
func safeJoin(base, rel string) (string, bool) {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) || filepath.IsAbs(rel) {
		return "", false
	}
	rel = filepath.FromSlash(rel)
	abs := filepath.Clean(filepath.Join(base, rel))
	cb := filepath.Clean(base)
	if abs != cb && !strings.HasPrefix(abs, cb+string(filepath.Separator)) {
		return "", false
	}
	return abs, true
}

// listDocs 列出目录内容（docmap 元数据，纯内存）：子目录 + 文件（行数/字节数/⭐索引标记）。
func (e *ToolExecutor) listDocs(args map[string]any, budget *ContextBudget) string {
	sub := argString(args, "path")
	if sub != "" {
		if _, ok := docRel(sub); !ok {
			return "路径无效（不允许 .. / 绝对路径），请用根目录列表中的路径。"
		}
	}
	prefix := strings.TrimSuffix(filepath.ToSlash(sub), "/")
	if prefix == "." {
		prefix = ""
	}

	m := e.docmap.Get()
	type entry struct {
		dir  bool
		name string
	}
	seen := map[string]entry{}
	order := []string{}
	add := func(name string, isDir bool) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = entry{dir: isDir, name: name}
		order = append(order, name)
	}

	// 直接子级：文件（dir == prefix 或 prefix 为空时 dir==""）与目录（下一段路径）
	for rel := range m.Files {
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = ""
		}
		if prefix == "" {
			if dir == "" {
				add(filepath.Base(rel), false) // 根下文件
			} else if !strings.Contains(dir, "/") {
				add(dir, true) // 根下子目录
			}
		} else {
			if dir == prefix {
				add(filepath.Base(rel), false)
			} else if strings.HasPrefix(dir, prefix+"/") {
				rest := strings.TrimPrefix(dir, prefix+"/")
				if !strings.Contains(rest, "/") {
					add(prefix+"/"+rest, true)
				}
			}
		}
	}
	// 目录优先，字典序
	sort.Slice(order, func(i, j int) bool {
		a, b := seen[order[i]], seen[order[j]]
		if a.dir != b.dir {
			return a.dir
		}
		return a.name < b.name
	})

	var sb strings.Builder
	head := "📚 文档目录"
	if prefix != "" {
		head += "：" + prefix + "/"
	}
	sb.WriteString(head + "\n")
	for _, name := range order {
		if sb.Len() > 0 && strings.Count(sb.String(), "\n") >= listDocsMaxLines {
			sb.WriteString("…（列表过长，请用 path 参数下钻子目录）\n")
			break
		}
		if seen[name].dir {
			sb.WriteString("📁 " + name + "/\n")
			continue
		}
		rel := name
		if prefix != "" {
			rel = prefix + "/" + name
		}
		f, ok := m.Files[rel]
		if !ok {
			continue
		}
		star := ""
		if f.IndexFile {
			star = " ⭐"
		}
		sb.WriteString(fmt.Sprintf("📄 %s（%d 行, %d 字节）%s\n", name, f.Lines, f.Bytes, star))
	}
	if strings.Count(sb.String(), "\n") <= 1 {
		sb.WriteString("（该目录为空）\n")
	}
	out := sb.String()
	budget.lightUsed += runeLen(out)
	return out
}

// getDocOutline 返回文档大纲（标题+行号），来自 docmap。
func (e *ToolExecutor) getDocOutline(args map[string]any, budget *ContextBudget) string {
	rel, ok := docRel(argString(args, "path"))
	if !ok {
		return "路径无效，请用 list_docs 查看可用文件。"
	}
	m := e.docmap.Get()
	f, ok := m.Files[rel]
	if !ok {
		return fmt.Sprintf("文件不存在：%s（请用 list_docs 查看可用文件）", rel)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📑 %s（共 %d 行）\n", rel, f.Lines))
	if len(f.Headings) == 0 {
		sb.WriteString("（该文件无标题结构，可直接 read_doc 阅读）\n")
	} else {
		for _, h := range f.Headings {
			sb.WriteString(fmt.Sprintf("第 %d 行 [%s] %s\n", h.Line, strings.Repeat("#", h.Level), h.Text))
		}
	}
	out := sb.String()
	budget.lightUsed += runeLen(out)
	return out
}

// readDoc 按行号范围读取文档（带行号前缀），受 readMax 预算与单页上限约束。
func (e *ToolExecutor) readDoc(args map[string]any, budget *ContextBudget) (string, error) {
	rel, ok := docRel(argString(args, "path"))
	if !ok {
		return "路径无效，请用 list_docs 查看可用文件。", nil
	}
	cfg := e.cfg().Agent
	if budget.readUsed >= budget.readMax {
		return "上下文预算已满，请基于已读内容直接作答（不要再读取文档）。", nil
	}
	abs, ok := safeJoin(e.docsDir, rel)
	if !ok {
		return "路径无效（不允许 .. / 绝对路径）。", nil
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf("读取失败：%s（请用 list_docs 确认文件存在）", rel), nil
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	start := argInt(args, "start_line", 1)
	if start < 1 {
		start = 1
	}
	maxLines := argInt(args, "max_lines", cfg.ReadPageLines)
	if maxLines < 1 || maxLines > cfg.ReadPageLines {
		maxLines = cfg.ReadPageLines
	}
	from := start - 1
	if from >= len(lines) {
		return fmt.Sprintf("（%s 共 %d 行，起始行 %d 超出范围）", rel, len(lines), start), nil
	}
	to := from + maxLines
	if to > len(lines) {
		to = len(lines)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("（%s 共 %d 行，显示第 %d~%d 行）\n", rel, len(lines), from+1, to))
	for i := from; i < to; i++ {
		sb.WriteString(fmt.Sprintf("%d: %s\n", i+1, lines[i]))
	}
	out := truncateRunes(sb.String(), cfg.ReadPageMaxChars)
	if runeLen(out) != runeLen(sb.String()) {
		out += "\n（已截断，请用 start_line/max_lines 翻页继续阅读）"
	}
	budget.readUsed += runeLen(out)
	return out, nil
}
