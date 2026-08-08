package civgo

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// scriptChat 按脚本响应的 chat 替身：每次 Complete 消费一步。
// step.calls 非空 → 返回工具调用；step.text 非空 → 返回直答文本；step.err → 返回错误。
type scriptChat struct {
	mu     sync.Mutex
	steps  []scriptStep
	cur    int
	inputs [][]InputItem // 记录每次调用收到的 input（供断言回填）
	tools  []bool        // 记录每次调用是否带工具
}

type scriptStep struct {
	calls []ToolCall
	text  string
	err   error
}

func newScriptChat(steps ...scriptStep) *scriptChat {
	return &scriptChat{steps: steps}
}

func (s *scriptChat) Complete(ctx context.Context, instructions string, input []InputItem, tools []Tool) (Completion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, input)
	s.tools = append(s.tools, len(tools) > 0)
	if s.cur >= len(s.steps) {
		return Completion{}, errors.New("scriptChat 步骤耗尽")
	}
	st := s.steps[s.cur]
	s.cur++
	if st.err != nil {
		return Completion{}, st.err
	}
	return Completion{Text: st.text, Calls: st.calls, Usage: Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

// toolCall 便捷构造。
func toolCall(id, name, args string) ToolCall {
	return ToolCall{CallID: id, Name: name, Arguments: args}
}

// testAgent 构造 agent（真实工具执行器 + scriptChat）。
func testAgent(t *testing.T, chat *scriptChat) *Agent {
	t.Helper()
	dir := writeDocTree(t)
	cfg := DefaultConfig()
	path := filepathJoinTemp(t)
	if _, err := BuildDocmap(dir, path, nil); err != nil {
		t.Fatal(err)
	}
	dm := NewDocmapStore(path)
	ex := NewToolExecutor(dm, dir, func() *Config { return cfg })
	return NewAgent(chat, ex, func() *Config { return cfg })
}

func filepathJoinTemp(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/civgo/docmap.json"
}

// TestAgentDirectAnswer AI 直接作答（不调工具）。
func TestAgentDirectAnswer(t *testing.T) {
	chat := newScriptChat(scriptStep{text: "弓手射程 2 格"})
	a := testAgent(t, chat)
	answer, usage, err := a.Run(context.Background(), "弓手射程多少？")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if answer != "弓手射程 2 格" {
		t.Errorf("回答错误: %q", answer)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 5 {
		t.Errorf("用量累计错误: %+v", usage)
	}
	// 只调了一次，带工具
	if len(chat.inputs) != 1 || !chat.tools[0] {
		t.Errorf("应恰好 1 次带工具的调用")
	}
}

// TestAgentToolLoop 工具调用 → 回填 → 再调用 → 直答。
func TestAgentToolLoop(t *testing.T) {
	chat := newScriptChat(
		scriptStep{calls: []ToolCall{toolCall("fc_1", "get_doc_outline", `{"path":"archer.md"}`)}},
		scriptStep{text: "根据大纲，弓手是远程单位。"},
	)
	a := testAgent(t, chat)
	answer, usage, err := a.Run(context.Background(), "弓手是什么？")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if !strings.Contains(answer, "远程单位") {
		t.Errorf("回答错误: %q", answer)
	}
	if usage.InputTokens != 20 || usage.OutputTokens != 10 {
		t.Errorf("两轮用量应累计: %+v", usage)
	}
	// 第二次调用 input 含 function_call 与 function_call_output 回填
	in := chat.inputs[1]
	var hasCall, hasOutput bool
	for _, item := range in {
		if item["type"] == "function_call" && item["call_id"] == "fc_1" {
			hasCall = true
		}
		if item["type"] == "function_call_output" && item["call_id"] == "fc_1" {
			hasOutput = true
		}
	}
	if !hasCall || !hasOutput {
		t.Errorf("应回填 function_call 与 function_call_output")
	}
	// 第二次调用带工具（循环内）
	if !chat.tools[1] {
		t.Errorf("循环内调用应带工具")
	}
}

// TestAgentMultipleCalls 单次响应多个 function_call 并发回填。
func TestAgentMultipleCalls(t *testing.T) {
	chat := newScriptChat(
		scriptStep{calls: []ToolCall{
			toolCall("fc_1", "get_doc_outline", `{"path":"archer.md"}`),
			toolCall("fc_2", "get_doc_outline", `{"path":"units/knight.md"}`),
		}},
		scriptStep{text: "两个都看了。"},
	)
	a := testAgent(t, chat)
	answer, _, err := a.Run(context.Background(), "对比一下")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if answer != "两个都看了。" {
		t.Errorf("回答错误: %q", answer)
	}
	// 第三次调用（直答前）input 应含 4 条工具相关条目
	in := chat.inputs[1]
	n := 0
	for _, item := range in {
		if item["type"] == "function_call" || item["type"] == "function_call_output" {
			n++
		}
	}
	if n != 4 {
		t.Errorf("两个调用应回填 4 条，got %d", n)
	}
}

// TestAgentMaxToolCalls 工具次数耗尽 → 强制直答（最后一次不带工具）。
func TestAgentMaxToolCalls(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agent.MaxToolCalls = 2
	dir := writeDocTree(t)
	dm := NewDocmapStore(t.TempDir() + "/dm.json")
	ex := NewToolExecutor(dm, dir, func() *Config { return cfg })
	chat := newScriptChat(
		scriptStep{calls: []ToolCall{toolCall("fc_1", "list_docs", "{}")}},
		scriptStep{calls: []ToolCall{toolCall("fc_2", "list_docs", "{}")}},
		scriptStep{calls: []ToolCall{toolCall("fc_3", "list_docs", "{}")}},
		scriptStep{text: "上限后作答"},
	)
	a := NewAgent(chat, ex, func() *Config { return cfg })
	answer, _, err := a.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if answer != "上限后作答" {
		t.Errorf("强制直答错误: %q", answer)
	}
	// 最后一次调用不应带工具（tools 已移除）
	if chat.tools[len(chat.tools)-1] {
		t.Errorf("工具耗尽后的调用不应带工具")
	}
	// 工具调用次数 = 2（第 3 次请求的调用被拒绝执行）
	if chat.cur != 4 {
		t.Errorf("应共 4 次请求，got %d", chat.cur)
	}
}

// TestAgentFallbackDirect 工具阶段失败 → 降级直答。
func TestAgentFallbackDirect(t *testing.T) {
	chat := newScriptChat(
		scriptStep{calls: []ToolCall{toolCall("fc_1", "get_doc_outline", `{"path":"archer.md"}`)}},
		scriptStep{err: errors.New("网络抖动")},
		scriptStep{text: "基于已读大纲回答"},
	)
	a := testAgent(t, chat)
	answer, _, err := a.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if !strings.Contains(answer, "已读大纲") {
		t.Errorf("降级直答错误: %q", answer)
	}
	// 降级调用不带工具
	if chat.tools[2] {
		t.Errorf("降级直答不应带工具")
	}
}

// TestAgentFirstCallFails 首轮即失败 → 直接报错（无降级）。
func TestAgentFirstCallFails(t *testing.T) {
	chat := newScriptChat(scriptStep{err: errors.New("网关挂了")})
	a := testAgent(t, chat)
	if _, _, err := a.Run(context.Background(), "q"); err == nil {
		t.Fatal("首轮失败应报错")
	}
}

// TestAgentToolErrorNoPanic 工具执行失败返回错误文本给 AI（不 panic）。
func TestAgentToolErrorNoPanic(t *testing.T) {
	chat := newScriptChat(
		scriptStep{calls: []ToolCall{toolCall("fc_1", "no_such_tool", "{}")}},
		scriptStep{text: "工具失败但继续"},
	)
	a := testAgent(t, chat)
	answer, _, err := a.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if answer != "工具失败但继续" {
		t.Errorf("工具失败不应中断: %q", answer)
	}
}
