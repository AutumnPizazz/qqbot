package rules

import (
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
)

// Engine 规则引擎：持有按群组织的编译后规则快照与检测状态（计数器/刷屏/重复文本），
// 每次事件按序匹配该群规则并执行动作。状态仅存内存（重启清零）。
type Engine struct {
	exec     Executor
	counters *counterStore
	floods   *floodDetector
	recent   *recentRing
	logger   *slog.Logger

	mu    sync.RWMutex
	rules map[int64][]*compiledRule // groupID → 编译后规则（按配置顺序）
}

// compiledRule 编译后的规则（条件/动作参数已解析，避免事件时重复解析）。
type compiledRule struct {
	rule  *Rule
	conds []compiledCond
	acts  []compiledAct
}

type compiledCond struct {
	negate bool
	typ    string
	eval   func(e *Engine, ctx *EventContext, p any) (bool, error)
	params any
}

type compiledAct struct {
	typ    string
	run    func(e *Engine, ctx *EventContext, x *ruleExec, p any) error
	params any
}

// New 创建引擎。exec 为动作执行器（bot 实现，走 ActionService + 审计）。
func New(exec Executor) *Engine {
	return &Engine{
		exec:     exec,
		counters: newCounterStore(),
		floods:   newFloodDetector(),
		recent:   newRecentRing(2000),
		logger:   slog.Default(),
		rules:    make(map[int64][]*compiledRule),
	}
}

// SetLogger 覆盖日志器（测试用）。
func (e *Engine) SetLogger(l *slog.Logger) {
	if l != nil {
		e.logger = l
	}
}

// SetRulesByGroup 编译并替换指定群的规则快照（groupID 之间完全隔离）。
// 任一规则编译失败：该群整体拒绝并保留旧快照。
func (e *Engine) SetRulesByGroup(rulesByGroup map[int64][]Rule) error {
	compiled := make(map[int64][]*compiledRule, len(rulesByGroup))
	for gid, rules := range rulesByGroup {
		grouped := make([]*compiledRule, 0, len(rules))
		for i := range rules {
			cr, err := compileOne(&rules[i])
			if err != nil {
				return err
			}
			grouped = append(grouped, cr)
		}
		compiled[gid] = grouped
	}
	e.mu.Lock()
	e.rules = compiled
	e.mu.Unlock()
	return nil
}

// compileOne 编译单条规则（参数解析失败即整体拒绝）。
func compileOne(r *Rule) (*compiledRule, error) {
	if err := ValidateRules([]Rule{*r}, "rule"); err != nil {
		return nil, err
	}
	cr := &compiledRule{rule: r}
	for i := range r.When {
		c := &r.When[i]
		spec := ConditionSpecs[c.Type]
		p, err := spec.Parse(c.Params)
		if err != nil {
			return nil, err
		}
		cr.conds = append(cr.conds, compiledCond{
			negate: c.Negate, typ: c.Type, eval: spec.Eval, params: p,
		})
	}
	for i := range r.Then {
		a := &r.Then[i]
		spec := ActionSpecs[a.Type]
		p, err := spec.Parse(a.Params)
		if err != nil {
			return nil, err
		}
		cr.acts = append(cr.acts, compiledAct{typ: a.Type, run: spec.Run, params: p})
	}
	return cr, nil
}

// ResetState 清空全部检测状态（配置热更新时调用，避免旧计数污染新规则）。
func (e *Engine) ResetState() {
	e.counters.ResetAll()
	e.floods.reset()
	e.recent.reset()
}

// Process 处理一次事件：登记消息状态 → 按序匹配规则 → 执行动作。
func (e *Engine) Process(ctx *EventContext) {
	if ctx == nil {
		return
	}
	// 消息事件先登记检测状态（刷屏窗口 / 重复文本），供条件只读查询
	if ctx.Event == EventMessage {
		e.floods.record(ctx.GroupID, ctx.UserID)
		e.recent.push(recentText{groupID: ctx.GroupID, userID: ctx.UserID, text: ctx.Text, time: ctx.Time})
	}
	e.mu.RLock()
	rules := e.rules[ctx.GroupID]
	e.mu.RUnlock()

	for _, cr := range rules {
		r := cr.rule
		if !r.Enabled || r.Event != ctx.Event {
			continue
		}
		matched, matchedText, err := e.evalRule(cr, ctx)
		if err != nil {
			e.logger.Warn("规则条件评估失败", "rule", r.Name, "err", err)
			continue
		}
		if !matched {
			continue
		}
		x := &ruleExec{rule: r, matched: matchedText}
		e.logger.Info("规则命中", "event", ctx.Event, "group", ctx.GroupID,
			"user", ctx.UserID, "rule", r.Name, "actions", len(cr.acts))
		for _, ca := range cr.acts {
			if err := ca.run(e, ctx, x, ca.params); err != nil {
				e.logger.Warn("规则动作执行失败",
					"rule", r.Name, "action", ca.typ, "err", err)
			}
		}
		if r.Break {
			break
		}
	}
}

// evalRule 评估条件：all=全部满足（AND）；any=任一满足（OR）。
// 条件支持逐条取反（Negate）。返回是否命中与首个命中条件的匹配文本。
func (e *Engine) evalRule(cr *compiledRule, ctx *EventContext) (bool, string, error) {
	r := cr.rule
	if len(cr.conds) == 0 {
		return true, "", nil // 空条件 = 恒真
	}
	anyMode := r.WhenMode == WhenModeAny
	matchedText := ""
	matchedAny := false
	for _, cc := range cr.conds {
		ok, err := cc.eval(e, ctx, cc.params)
		if err != nil {
			return false, "", err
		}
		if cc.negate {
			ok = !ok
		}
		if ok {
			matchedAny = true
			if matchedText == "" {
				matchedText = conditionMatchedText(cc.typ, cc.params, ctx)
			}
			if anyMode {
				return true, matchedText, nil // OR：任一满足即命中
			}
		} else if !anyMode {
			return false, "", nil // AND：任一不满足即失败
		}
	}
	return matchedAny, matchedText, nil
}

// conditionMatchedText 提取条件的匹配文本（{matched_text} 变量来源）。
// 文本类条件返回其参数（关键词/正则原文），与旧版"命中即展示规则 pattern"行为一致。
func conditionMatchedText(typ string, params any, ctx *EventContext) string {
	switch typ {
	case "text_contains", "request_contains":
		if p, ok := params.(*TextContainsParams); ok {
			return p.Text
		}
	case "text_regex", "request_regex":
		if p, ok := params.(*TextRegexParams); ok {
			return p.Pattern
		}
	}
	_ = ctx
	return ""
}

// CounterSnapshot 导出计数器状态（网页排障面板）。返回 key=群/计数器 → 用户 → 次数。
func (e *Engine) CounterSnapshot() map[string]map[int64]int {
	return e.counters.Snapshot()
}

// DecodeRuleParams 解析单条条件/动作参数（供 admin 层复用，返回 nil 表示无参数）。
func DecodeRuleParams(isCondition bool, typ string, raw json.RawMessage) (any, error) {
	if isCondition {
		spec, ok := ConditionSpecs[typ]
		if !ok {
			return nil, paramErrf("未知条件类型 %q", typ)
		}
		return spec.Parse(raw)
	}
	spec, ok := ActionSpecs[typ]
	if !ok {
		return nil, paramErrf("未知动作类型 %q", typ)
	}
	return spec.Parse(raw)
}

// ---- rule-meta API 元数据 ----

// MetaEvent 一个事件可用的条件/动作类型（前端过滤）。
type MetaEvent struct {
	Name       string   `json:"name"`
	Label      string   `json:"label"`
	Conditions []string `json:"conditions"`
	Actions    []string `json:"actions"`
}

// MetaType 条件/动作类型的注册表条目（含参数 JSON schema）。
type MetaType struct {
	Type         string         `json:"type"`
	Label        string         `json:"label"`
	Desc         string         `json:"desc,omitempty"`
	Events       []string       `json:"events"`
	Dangerous    bool           `json:"dangerous,omitempty"`
	ParamsSchema map[string]any `json:"params_schema,omitempty"`
}

// Meta 规则引擎注册表元数据（GET /api/v1/rule-meta）。
type Meta struct {
	Events     []MetaEvent `json:"events"`
	Conditions []MetaType  `json:"conditions"`
	Actions    []MetaType  `json:"actions"`
}

// RuleMeta 构建注册表元数据（类型按名字排序，保证前端下拉顺序稳定）。
func RuleMeta() Meta {
	events := []MetaEvent{
		{Name: EventMessage, Label: "群消息", Conditions: []string{}, Actions: []string{}},
		{Name: EventGroupIncrease, Label: "新人入群", Conditions: []string{}, Actions: []string{}},
		{Name: EventGroupRequest, Label: "加群申请", Conditions: []string{}, Actions: []string{}},
	}
	condKeys := make([]string, 0, len(ConditionSpecs))
	for k := range ConditionSpecs {
		condKeys = append(condKeys, k)
	}
	sort.Strings(condKeys)
	conds := make([]MetaType, 0, len(ConditionSpecs))
	for _, k := range condKeys {
		spec := ConditionSpecs[k]
		conds = append(conds, MetaType{
			Type: spec.Type, Label: spec.Label, Desc: spec.Desc,
			Events: spec.Events, ParamsSchema: schemaOf(spec.Zero),
		})
		for i := range events {
			if eventAllowed(spec.Events, events[i].Name) {
				events[i].Conditions = append(events[i].Conditions, spec.Type)
			}
		}
	}
	actKeys := make([]string, 0, len(ActionSpecs))
	for k := range ActionSpecs {
		actKeys = append(actKeys, k)
	}
	sort.Strings(actKeys)
	acts := make([]MetaType, 0, len(ActionSpecs))
	for _, k := range actKeys {
		spec := ActionSpecs[k]
		acts = append(acts, MetaType{
			Type: spec.Type, Label: spec.Label, Desc: spec.Desc,
			Events: spec.Events, Dangerous: spec.Dangerous, ParamsSchema: schemaOf(spec.Zero),
		})
		for i := range events {
			if eventAllowed(spec.Events, events[i].Name) {
				events[i].Actions = append(events[i].Actions, spec.Type)
			}
		}
	}
	return Meta{Events: events, Conditions: conds, Actions: acts}
}
