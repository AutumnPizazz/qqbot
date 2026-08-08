package civgo

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Agent AI 自主检索代理：基于 function calling 的多轮工具循环。
// AI 自行决定读哪些文档、读多少，直到直接作答或工具次数耗尽。
type Agent struct {
	chat  ChatCompleter
	tools *ToolExecutor
	cfg   func() *Config // 热重载快照
}

// NewAgent 创建代理。
func NewAgent(chat ChatCompleter, tools *ToolExecutor, cfg func() *Config) *Agent {
	return &Agent{chat: chat, tools: tools, cfg: cfg}
}

// Run 执行一次问答：工具调用循环直到 AI 直接作答。
// groupID 为提问所在群（recall_history 按群召回）。
// 返回最终回答文本与累计 token 用量（含全部工具轮次）。
// 保护机制：max_tool_calls 轮工具上限、累计上下文预算（tools 内执行）、
// 中途 AI 调用失败时已读文档则降级直答一次。
func (a *Agent) Run(ctx context.Context, q string, groupID int64) (string, Usage, error) {
	cfg := a.cfg()
	budget := newContextBudget(cfg.Agent)
	input := []InputItem{{"role": "user", "content": q}}
	tools := a.tools.Definitions()
	var total Usage
	start := time.Now()

	for turn := 0; ; turn++ {
		comp, err := a.chat.Complete(ctx, systemPrompt, input, tools)
		if err != nil {
			return a.fallbackDirect(ctx, input, total, start, err)
		}
		total.InputTokens += comp.Usage.InputTokens
		total.OutputTokens += comp.Usage.OutputTokens

		if len(comp.Calls) == 0 {
			// AI 直接作答
			if comp.Text == "" {
				return "", total, fmt.Errorf("responses 输出为空")
			}
			return comp.Text, total, nil
		}

		if turn >= cfg.Agent.MaxToolCalls {
			// 工具次数耗尽：提示后强制直答（不再允许工具）
			slog.Warn("civgo 工具调用达上限，强制直答", "turns", turn+1, "q", truncateRunes(q, 50))
			input = append(input, InputItem{
				"role":    "system",
				"content": "工具调用次数已达上限，请基于已获取的资料直接回答，不要再调用工具。",
			})
			comp2, rerr := a.chat.Complete(ctx, systemPrompt, input, nil)
			if rerr != nil {
				return "", total, rerr
			}
			total.InputTokens += comp2.Usage.InputTokens
			total.OutputTokens += comp2.Usage.OutputTokens
			if comp2.Text == "" {
				return "", total, fmt.Errorf("工具耗尽后直答为空")
			}
			slog.Info("civgo 问答完成（工具上限强制直答）", "q", truncateRunes(q, 50),
				"in_tok", total.InputTokens, "out_tok", total.OutputTokens,
				"ms", time.Since(start).Milliseconds())
			return comp2.Text, total, nil
		}

		// 执行工具调用并回填
		for _, c := range comp.Calls {
			out, xerr := a.tools.Execute(c.Name, c.Arguments, budget, groupID)
			if xerr != nil {
				out = "工具执行失败：" + xerr.Error()
			}
			slog.Info("civgo 工具调用", "tool", c.Name, "args", truncateStr(c.Arguments, 150),
				"out_chars", runeLen(out), "turn", turn)
			input = append(input, InputItem{
				"type": "function_call", "call_id": c.CallID,
				"name": c.Name, "arguments": c.Arguments,
			})
			input = append(input, InputItem{
				"type": "function_call_output", "call_id": c.CallID, "output": out,
			})
		}
	}
}

// fallbackDirect 工具阶段失败时的降级：
// 已发生工具调用（读过文档）→ 用已读上下文直答重试一次；否则直接返回错误。
func (a *Agent) fallbackDirect(ctx context.Context, input []InputItem, total Usage, start time.Time, err error) (string, Usage, error) {
	if len(input) < 3 {
		// 无任何工具结果：直接失败（群内友好提示）
		return "", total, err
	}
	slog.Warn("civgo 工具阶段失败，尝试直答降级", "err", err)
	input = append(input, InputItem{
		"role":    "system",
		"content": "工具调用失败，请基于已获取的资料直接回答（不要再调用工具）。若资料不足请如实说明。",
	})
	comp, rerr := a.chat.Complete(ctx, systemPrompt, input, nil)
	if rerr != nil {
		return "", total, rerr
	}
	total.InputTokens += comp.Usage.InputTokens
	total.OutputTokens += comp.Usage.OutputTokens
	if comp.Text == "" {
		return "", total, fmt.Errorf("降级直答为空")
	}
	slog.Info("civgo 问答完成（降级直答）", "in_tok", total.InputTokens,
		"out_tok", total.OutputTokens, "ms", time.Since(start).Milliseconds())
	return comp.Text, total, nil
}
