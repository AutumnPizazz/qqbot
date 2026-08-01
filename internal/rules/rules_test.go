package rules

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockExec 记录动作调用（引擎测试用）。
type mockExec struct {
	mu    sync.Mutex
	mutes []string // "group/user/minutes/notify/note"
	kicks []string
	warns []string
	recs  []int32
	sends []string
	appr  []string
	rejc  []string
	bans  []string
	cards []string
	err   error // 非 nil 时所有动作返回该错误（模拟连接失败）
}

func newMockExec() *mockExec { return &mockExec{} }

func (m *mockExec) fail(err error) *mockExec { m.err = err; return m }

func (m *mockExec) Mute(g, u int64, minutes int, notify, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mutes = append(m.mutes, fmt.Sprintf("%d/%d/%d/%s/%s", g, u, minutes, notify, note))
	return m.err
}
func (m *mockExec) Kick(g, u int64, notify, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kicks = append(m.kicks, fmt.Sprintf("%d/%d/%s/%s", g, u, notify, note))
	return m.err
}
func (m *mockExec) Warn(g, u int64, text, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.warns = append(m.warns, fmt.Sprintf("%d/%d/%s/%s", g, u, text, note))
	return m.err
}
func (m *mockExec) Recall(id int32, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, id)
	return m.err
}
func (m *mockExec) Send(g int64, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sends = append(m.sends, fmt.Sprintf("%d/%s", g, text))
	return m.err
}
func (m *mockExec) SendAt(g, u int64, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sends = append(m.sends, fmt.Sprintf("%d/%d@%s", g, u, text))
	return m.err
}
func (m *mockExec) ApproveJoin(g, u int64, flag, sub string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appr = append(m.appr, fmt.Sprintf("%d/%d/%s/%s", g, u, flag, sub))
	return m.err
}
func (m *mockExec) RejectJoin(g, u int64, flag, sub, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rejc = append(m.rejc, fmt.Sprintf("%d/%d/%s/%s/%s", g, u, flag, sub, reason))
	return m.err
}
func (m *mockExec) SendPrivate(u int64, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sends = append(m.sends, fmt.Sprintf("私聊/%d/%s", u, text))
	return m.err
}
func (m *mockExec) WholeBan(g int64, enable bool, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bans = append(m.bans, fmt.Sprintf("%d/%v/%s", g, enable, note))
	return m.err
}
func (m *mockExec) Card(g, u int64, card, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cards = append(m.cards, fmt.Sprintf("%d/%d/%s/%s", g, u, card, note))
	return m.err
}

// ---- 参数解析与校验 ----

func TestParseConditionParams(t *testing.T) {
	cases := []struct {
		typ     string
		params  string
		wantErr bool
	}{
		{"text_contains", `{"text": "广告"}`, false},
		{"text_contains", `{"text": ""}`, true},
		{"text_contains", `{"text": "x", "extra": 1}`, true}, // 未知字段
		{"text_contains", ``, true},                          // 必填缺失
		{"text_regex", `{"pattern": "["}`, true},             // 正则无效
		{"text_regex", `{"pattern": "^a+$"}`, false},
		{"flood", `{"window_sec": 1, "max_count": 8}`, true}, // 窗口越界
		{"flood", `{"window_sec": 10, "max_count": 8}`, false},
		{"strike_count", `{"counter_id": "kw", "min_count": 3, "window_hours": 24}`, false},
		{"strike_count", `{"counter_id": "bad id!", "min_count": 3, "window_hours": 24}`, true},
		{"time_between", `{"start": "23:00", "end": "07:00"}`, false},
		{"time_between", `{"start": "25:00", "end": "07:00"}`, true},
		{"user_role", `{"roles": ["owner"]}`, false},
		{"user_role", `{"roles": ["superman"]}`, true},
		{"subtype_is", `{"subtype": "invite"}`, false},
		{"unknown_type", `{}`, true},
	}
	for _, tc := range cases {
		_, err := validateConditionParams(tc.typ, json.RawMessage(tc.params))
		if (err != nil) != tc.wantErr {
			t.Errorf("%s(%s) err=%v wantErr=%v", tc.typ, tc.params, err, tc.wantErr)
		}
	}
	// 无参数类型
	for _, typ := range []string{"always", "is_exempt", "in_whitelist"} {
		if _, err := validateConditionParams(typ, nil); err != nil {
			t.Errorf("%s 应接受无参数: %v", typ, err)
		}
	}
}

func TestParseActionParams(t *testing.T) {
	cases := []struct {
		typ     string
		params  string
		wantErr bool
	}{
		{"mute", `{"minutes": 10}`, false},
		{"mute", `{"minutes": 0}`, true},
		{"mute", `{"minutes": 999999}`, true},
		{"kick", `{"reason": "违规"}`, false},
		{"send_message", `{"message": "hi"}`, false},
		{"send_message", `{"message": ""}`, true},
		{"increment_counter", `{"counter_id": "kw", "step": 1, "window_hours": 24}`, false},
		{"increment_counter", `{"counter_id": "kw"}`, true}, // window_hours 必填
		{"reset_counter", `{"counter_id": "kw"}`, false},
		{"set_counter", `{"counter_id": "kw", "count": 5}`, false},
		{"warn", `{"reason": "禁止"}`, false},
		{"warn", `{"reason": ""}`, true},
		{"unknown_type", `{}`, true},
	}
	for _, tc := range cases {
		_, err := validateActionParams(tc.typ, json.RawMessage(tc.params))
		if (err != nil) != tc.wantErr {
			t.Errorf("%s(%s) err=%v wantErr=%v", tc.typ, tc.params, err, tc.wantErr)
		}
	}
}

// ---- 校验 ----

func TestValidateRules(t *testing.T) {
	good := func(id, name string) Rule {
		return Rule{ID: id, Name: name, Enabled: true, Event: EventMessage,
			Then: []Action{{Type: "noop"}}, Break: true}
	}
	// 合法
	if err := ValidateRules([]Rule{good("r1", "a")}, "rules"); err != nil {
		t.Fatalf("合法规则不应失败: %v", err)
	}
	// ID/名称重复
	err := ValidateRules([]Rule{good("r1", "a"), good("r1", "a")}, "rules")
	if err == nil || len(err.Fields) != 2 {
		t.Fatalf("重复应报 2 个错误: %v", err)
	}
	// 事件不匹配（warn 动作用于入群事件）
	r := good("r1", "a")
	r.Event = EventGroupIncrease
	r.Then = []Action{{Type: "warn"}}
	err = ValidateRules([]Rule{r}, "rules")
	if err == nil || len(err.Fields) == 0 {
		t.Fatal("事件不匹配应报错")
	}
	// 条件数量上限
	r = good("r1", "a")
	for i := 0; i < MaxConditionsPerRule+1; i++ {
		r.When = append(r.When, Condition{Type: "always"})
	}
	err = ValidateRules([]Rule{r}, "rules")
	if err == nil || len(err.Fields) == 0 {
		t.Fatal("条件超限应报错")
	}
}

// ---- 引擎语义 ----

func newTestEngine(t *testing.T, rules []Rule) (*Engine, *mockExec) {
	t.Helper()
	m := newMockExec()
	e := New(m)
	if err := e.SetRulesByGroup(map[int64][]Rule{100: rules}); err != nil {
		t.Fatalf("SetRulesByGroup 失败: %v", err)
	}
	return e, m
}

func msgCtx(text, role string) *EventContext {
	return &EventContext{
		Event: EventMessage, GroupID: 100, UserID: 200, Role: role,
		Text: text, Time: time.Now(), IsOwner: false, BotName: "bot",
	}
}

func TestEngineKeywordWarn(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "广告", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})}},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "禁止广告"})}},
		Break: true,
	}})
	e.Process(msgCtx("这里有广告吗", "member"))
	if len(m.warns) != 1 || !strings.Contains(m.warns[0], "警告：禁止广告") {
		t.Fatalf("warn 未执行: %v", m.warns)
	}
	// 大小写不敏感
	e.Process(msgCtx("広告", "member"))
	if len(m.warns) != 1 {
		t.Fatal("不应误命中")
	}
	// 不匹配
	e.Process(msgCtx("正常消息", "member"))
	if len(m.warns) != 1 {
		t.Fatal("不应命中")
	}
}

func TestEngineBreakContinue(t *testing.T) {
	// 规则1 break=false（计数），规则2 break=true（警告）
	e, m := newTestEngine(t, []Rule{
		{
			ID: "r1", Name: "计数", Enabled: true, Event: EventMessage,
			When: []Condition{{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})}},
			Then: []Action{{Type: "increment_counter", Params: mustJSON(CounterParams{CounterID: "kw", Step: 1, WindowHours: 24})}},
			Break: false,
		},
		{
			ID: "r2", Name: "警告", Enabled: true, Event: EventMessage,
			When: []Condition{{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})}},
			Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "广告", CounterID: "kw"})}},
			Break: true,
		},
	})
	e.Process(msgCtx("广告", "member"))
	if len(m.warns) != 1 {
		t.Fatalf("continue 规则未继续匹配: %v", m.warns)
	}
	// 第三条规则（break 后不执行）
	// warn 文案中的 {count} 来自计数器
	if !strings.Contains(m.warns[0], "广告") {
		t.Fatalf("warn 文案错误: %v", m.warns[0])
	}
}

func TestEngineUpgradePattern(t *testing.T) {
	// 设计文档 §6.1：第 3 次命中才禁言并清零
	rules := []Rule{
		{
			ID: "c", Name: "计数", Enabled: true, Event: EventMessage,
			When: []Condition{{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})}},
			Then: []Action{{Type: "increment_counter", Params: mustJSON(CounterParams{CounterID: "kw", Step: 1, WindowHours: 24})}},
		},
		{
			ID: "up", Name: "升级", Enabled: true, Event: EventMessage,
			When: []Condition{
				{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})},
				{Type: "strike_count", Params: mustJSON(StrikeCountParams{CounterID: "kw", MinCount: 3, WindowHours: 24})},
			},
			Then: []Action{
				{Type: "mute", Params: mustJSON(MuteParams{Minutes: 30, Reason: "多次广告"})},
				{Type: "reset_counter", Params: mustJSON(CounterParams{CounterID: "kw"})},
			},
			Break: true,
		},
		{
			ID: "w", Name: "警告", Enabled: true, Event: EventMessage,
			When: []Condition{{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})}},
			Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "禁止广告"})}},
			Break: true,
		},
	}
	e, m := newTestEngine(t, rules)
	for i := 0; i < 2; i++ {
		e.Process(msgCtx("广告", "member"))
	}
	if len(m.warns) != 2 || len(m.mutes) != 0 {
		t.Fatalf("前 2 次应仅警告: warns=%v mutes=%v", len(m.warns), len(m.mutes))
	}
	e.Process(msgCtx("广告", "member"))
	if len(m.warns) != 2 || len(m.mutes) != 1 {
		t.Fatalf("第 3 次应升级禁言: warns=%v mutes=%v", len(m.warns), len(m.mutes))
	}
	// 清零后重新计数
	e.Process(msgCtx("广告", "member"))
	if len(m.warns) != 3 {
		t.Fatalf("清零后应从警告重新开始: %v", m.warns)
	}
}

func TestEngineFloodUpgrade(t *testing.T) {
	rules := []Rule{
		{
			ID: "up", Name: "刷屏多次移出", Enabled: true, Event: EventMessage,
			When: []Condition{
				{Type: "flood", Params: mustJSON(FloodParams{WindowSec: 3600, MaxCount: 3})},
				{Type: "strike_count", Params: mustJSON(StrikeCountParams{CounterID: "flood", MinCount: 2, WindowHours: 24})},
			},
			Then: []Action{
				{Type: "kick", Params: mustJSON(KickParams{Reason: "多次刷屏"})},
				{Type: "reset_counter", Params: mustJSON(CounterParams{CounterID: "flood"})},
			},
			Break: true,
		},
		{
			ID: "f", Name: "刷屏", Enabled: true, Event: EventMessage,
			When: []Condition{{Type: "flood", Params: mustJSON(FloodParams{WindowSec: 3600, MaxCount: 3})}},
			Then: []Action{
				{Type: "mute", Params: mustJSON(MuteParams{Minutes: 10, Reason: "刷屏"})},
				{Type: "increment_counter", Params: mustJSON(CounterParams{CounterID: "flood", Step: 1, WindowHours: 24})},
			},
			Break: true,
		},
	}
	e, m := newTestEngine(t, rules)
	// 窗口 3600s 内：第 1/2 条未达阈值；第 3/4 条禁言并计数；第 5 条累计 2 次刷屏 → 移出
	e.Process(msgCtx("1", "member"))
	e.Process(msgCtx("2", "member"))
	if len(m.mutes) != 0 {
		t.Fatal("未达阈值不应处罚")
	}
	e.Process(msgCtx("3", "member"))
	e.Process(msgCtx("4", "member"))
	if len(m.mutes) != 2 {
		t.Fatalf("第 3/4 条应禁言: %v", m.mutes)
	}
	e.Process(msgCtx("5", "member"))
	if len(m.mutes) != 2 || len(m.kicks) != 1 {
		t.Fatalf("第 2 次刷屏应移出: mutes=%v kicks=%v", len(m.mutes), len(m.kicks))
	}
}

func TestEngineFilterRule(t *testing.T) {
	// 过滤器规则：管理员豁免，无动作 + break
	e, m := newTestEngine(t, []Rule{
		{ID: "ex", Name: "豁免", Enabled: true, Event: EventMessage,
			When: []Condition{{Type: "is_exempt"}}, Then: nil, Break: true},
		{ID: "w", Name: "警告", Enabled: true, Event: EventMessage,
			When: []Condition{{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})}},
			Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "x"})}}, Break: true},
	})
	// 管理员被豁免
	ctx := msgCtx("广告", "admin")
	e.Process(ctx)
	if len(m.warns) != 0 {
		t.Fatal("管理员应被豁免")
	}
	// 白名单被豁免
	ctx = msgCtx("广告", "member")
	ctx.IsWhitelisted = func() bool { return true }
	e.Process(ctx)
	if len(m.warns) != 0 {
		t.Fatal("白名单应被豁免")
	}
	// 普通成员处罚
	e.Process(msgCtx("广告", "member"))
	if len(m.warns) != 1 {
		t.Fatal("普通成员应被处罚")
	}
}

func TestEngineNegate(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "非管理员广告", Enabled: true, Event: EventMessage,
		When: []Condition{
			{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})},
			{Type: "user_role", Params: mustJSON(UserRoleParams{Roles: []string{"admin"}}), Negate: true},
		},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "x"})}},
		Break: true,
	}})
	e.Process(msgCtx("广告", "member"))
	if len(m.warns) != 1 {
		t.Fatal("普通成员应命中")
	}
	e.Process(msgCtx("广告", "admin"))
	if len(m.warns) != 1 {
		t.Fatal("管理员应被 negate 排除")
	}
}

func TestEngineTimeBetween(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "深夜禁言", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "time_between", Params: mustJSON(TimeBetweenParams{Start: "23:00", End: "07:00"})}},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "深夜"})}},
		Break: true,
	}})
	day := time.Date(2026, 8, 6, 12, 0, 0, 0, time.Local)
	night := time.Date(2026, 8, 6, 23, 30, 0, 0, time.Local)
	midnight := time.Date(2026, 8, 7, 2, 0, 0, 0, time.Local)
	ctx := msgCtx("x", "member")
	ctx.Time = day
	e.Process(ctx)
	if len(m.warns) != 0 {
		t.Fatal("白天不应命中")
	}
	ctx.Time = night
	e.Process(ctx)
	if len(m.warns) != 1 {
		t.Fatal("深夜应命中")
	}
	ctx.Time = midnight
	e.Process(ctx)
	if len(m.warns) != 2 {
		t.Fatal("跨午夜应命中")
	}
}

func TestEngineTextRepeat(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "复读", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "text_repeat", Params: mustJSON(TextRepeatParams{WindowSec: 60, MinCount: 3})}},
		Then: []Action{{Type: "mute", Params: mustJSON(MuteParams{Minutes: 10, Reason: "复读"})}},
		Break: true,
	}})
	e.Process(msgCtx("哈哈哈", "member"))
	e.Process(msgCtx("哈哈哈", "member"))
	if len(m.mutes) != 0 {
		t.Fatal("2 次不应处罚")
	}
	e.Process(msgCtx("哈哈哈", "member"))
	if len(m.mutes) != 1 {
		t.Fatal("第 3 次相同文本应处罚")
	}
	// 不同文本不计数
	e.Process(msgCtx("嘿嘿嘿", "member"))
	if len(m.mutes) != 1 {
		t.Fatal("不同文本不应处罚")
	}
}

func TestEngineMessageHasType(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "禁图", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "message_has_type", Params: mustJSON(MessageHasTypeParams{Types: []string{"image"}})}},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "禁图"})}},
		Break: true,
	}})
	ctx := msgCtx("看图", "member")
	ctx.MsgTypes = []string{"text", "image"}
	e.Process(ctx)
	if len(m.warns) != 1 {
		t.Fatal("含图片应命中")
	}
	e.Process(msgCtx("看图", "member"))
	if len(m.warns) != 1 {
		t.Fatal("纯文本不应命中")
	}
}

func TestEngineRecall(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "撤回", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})}},
		Then: []Action{{Type: "recall"}},
		Break: true,
	}})
	ctx := msgCtx("广告", "member")
	ctx.MessageID = 123
	e.Process(ctx)
	if len(m.recs) != 1 || m.recs[0] != 123 {
		t.Fatalf("撤回未执行: %v", m.recs)
	}
}

func TestEngineGroupIncrease(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "欢迎", Enabled: true, Event: EventGroupIncrease,
		Then: []Action{{Type: "send_message", Params: mustJSON(SendMessageParams{
			Message: "欢迎 {nickname} 来到 {group_id}（{bot_name}）", At: true,
		})}},
		Break: true,
	}})
	ctx := &EventContext{Event: EventGroupIncrease, GroupID: 100, UserID: 300, Time: time.Now(), BotName: "bot"}
	ctx.Nickname = func() string { return "小明" }
	e.Process(ctx)
	if len(m.sends) != 1 {
		t.Fatalf("欢迎未发送: %v", m.sends)
	}
	if !strings.Contains(m.sends[0], "100/300@欢迎 小明 来到 100（bot）") {
		t.Fatalf("模板替换错误: %v", m.sends[0])
	}
}

func TestEngineGroupRequest(t *testing.T) {
	e, m := newTestEngine(t, []Rule{
		{
			ID: "r1", Name: "拒绝中介", Enabled: true, Event: EventGroupRequest,
			When: []Condition{{Type: "request_contains", Params: mustJSON(TextContainsParams{Text: "中介"})}},
			Then: []Action{{Type: "reject_join", Params: mustJSON(RejectJoinParams{Reason: "不允许"})}},
			Break: true,
		},
		{
			ID: "r2", Name: "同意", Enabled: true, Event: EventGroupRequest,
			When: []Condition{{Type: "subtype_is", Params: mustJSON(SubtypeParams{Subtype: "add"})}},
			Then: []Action{{Type: "approve_join"}},
			Break: true,
		},
	})
	req := func(text, sub string) *EventContext {
		return &EventContext{Event: EventGroupRequest, GroupID: 100, UserID: 400,
			Text: text, SubType: sub, Flag: "flag1", Time: time.Now()}
	}
	e.Process(req("想加群", "add"))
	if len(m.appr) != 1 {
		t.Fatal("正常申请应同意")
	}
	e.Process(req("我是中介", "add"))
	if len(m.appr) != 1 || len(m.rejc) != 1 || !strings.Contains(m.rejc[0], "不允许") {
		t.Fatalf("中介申请应拒绝: appr=%v rejc=%v", m.appr, m.rejc)
	}
	// 邀请：非 owner 拒绝（is_owner 条件规则缺省）
	e.Process(req("", "invite"))
	if len(m.rejc) != 1 {
		t.Fatal("默认不应拒绝邀请（无规则时）")
	}
}

func TestEngineActionFailureContinues(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "组合", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})}},
		Then: []Action{
			{Type: "mute", Params: mustJSON(MuteParams{Minutes: 10})},
			{Type: "warn", Params: mustJSON(WarnParams{Reason: "x"})},
		},
		Break: true,
	}})
	m.fail(fmt.Errorf("连接不可用"))
	e.Process(msgCtx("广告", "member"))
	// 两个动作都失败但都执行了（不中断）
	if len(m.mutes) != 1 || len(m.warns) != 1 {
		t.Fatalf("动作失败不应中断后续: mutes=%v warns=%v", len(m.mutes), len(m.warns))
	}
}

func TestEngineDisabledRuleSkipped(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "停用", Enabled: false, Event: EventMessage,
		When: []Condition{{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})}},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "x"})}},
		Break: true,
	}})
	e.Process(msgCtx("广告", "member"))
	if len(m.warns) != 0 {
		t.Fatal("停用规则不应执行")
	}
}

func TestEngineSetRulesRejectsInvalid(t *testing.T) {
	e, _ := newTestEngine(t, []Rule{{ID: "r1", Name: "a", Enabled: true, Event: EventMessage,
		Then: []Action{{Type: "noop"}}, Break: true}})
	bad := []Rule{{ID: "r2", Name: "b", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "text_regex", Params: mustJSON(TextRegexParams{Pattern: "["})}},
		Then: []Action{{Type: "noop"}}, Break: true}}
	if err := e.SetRulesByGroup(map[int64][]Rule{100: bad}); err == nil {
		t.Fatal("非法规则应被拒绝")
	}
	// 旧规则保留
	m := newMockExec()
	e.exec = m
	e.Process(msgCtx("x", "member"))
	// 旧规则 noop 无动作，仅验证不 panic
}

// 群间规则隔离：A 群规则不得处理 B 群消息。
func TestEngineGroupIsolation(t *testing.T) {
	m := newMockExec()
	e := New(m)
	warnRule := func(id, name, keyword, reason string) Rule {
		return Rule{ID: id, Name: name, Enabled: true, Event: EventMessage,
			When: []Condition{{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: keyword})}},
			Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: reason})}},
			Break: true,
		}
	}
	err := e.SetRulesByGroup(map[int64][]Rule{
		100: {warnRule("r1", "广告", "广告", "x")},
		200: {{ID: "r2", Name: "无事", Enabled: true, Event: EventMessage,
			Then: []Action{{Type: "noop"}}, Break: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.Process(msgCtx("广告", "member")) // 群 100
	if len(m.warns) != 1 {
		t.Fatal("群 100 应命中")
	}
	// 群 200 不应命中群 100 的规则
	ctx := msgCtx("广告", "member")
	ctx.GroupID = 200
	e.Process(ctx)
	if len(m.warns) != 1 {
		t.Fatal("群间规则不得串扰")
	}
}
// ---- 计数器窗口 ----

func TestCounterWindow(t *testing.T) {
	s := newCounterStore()
	// 窗口 1 秒
	n := s.Increment(1, "kw", 100, 1, time.Second)
	if n != 1 {
		t.Fatalf("计数应 1: %d", n)
	}
	n = s.Increment(1, "kw", 100, 1, time.Second)
	if n != 2 {
		t.Fatalf("计数应 2: %d", n)
	}
	// Get 窗口内
	if s.Get(1, "kw", 100, time.Second) != 2 {
		t.Fatal("Get 应返回 2")
	}
	// 窗口滑动（手动改时间戳模拟）
	s.mu.Lock()
	s.data["1/kw"][100].updated = time.Now().Add(-2 * time.Second)
	s.mu.Unlock()
	n = s.Increment(1, "kw", 100, 1, time.Second)
	if n != 1 {
		t.Fatalf("窗口过期后应重置为 1: %d", n)
	}
	// 不同用户隔离
	n = s.Increment(1, "kw", 200, 1, time.Second)
	if n != 1 {
		t.Fatalf("不同用户应独立: %d", n)
	}
	// 不同群隔离
	n = s.Increment(2, "kw", 100, 1, time.Second)
	if n != 1 {
		t.Fatalf("不同群应独立: %d", n)
	}
	// Reset / Set
	s.Reset(1, "kw", 100)
	if s.Get(1, "kw", 100, time.Second) != 0 {
		t.Fatal("Reset 后应为 0")
	}
	s.Set(1, "kw", 100, 7)
	if s.Get(1, "kw", 100, time.Second) != 7 {
		t.Fatal("Set 后应为 7")
	}
}

// ---- 迁移黄金测试 ----

func TestMigrateV1Keyword(t *testing.T) {
	seq := 0
	rules := MigrateV1(nil, &V1Keyword{
		Enabled: true, WarnLimit: 3, StrikeTTLH: 24,
		Rules: []V1KeywordRule{
			{Pattern: "广告", Action: "warn"},
			{Pattern: "^开挂", Regex: true, Action: "mute", MuteMinutes: 60},
			{Pattern: "诈骗", Action: "kick"},
		},
	}, nil, nil, &seq)
	// 3 关键词（广告 warn 有升级；开挂/诈骗 无升级）+ 豁免 + 邀请×2 = 3+4 = 7 条
	if len(rules) != 7 {
		t.Fatalf("规则数量错误: %d %v", len(rules), ruleNames(rules))
	}
	// 首条是豁免
	if rules[0].Name != "默认豁免" || rules[0].Event != EventMessage || len(rules[0].Then) != 0 || !rules[0].Break {
		t.Fatalf("豁免规则错误: %+v", rules[0])
	}
	// 升级规则在普通规则之前（广告 warn_limit=3 生成升级）
	names := ruleNames(rules)
	if names[3] != "关键词-广告升级禁言" || names[4] != "关键词-广告" {
		t.Fatalf("升级规则顺序错误: %v", names)
	}
	// 正则 mute 规则动作含计数（无升级）
	mute := rules[5]
	if mute.When[0].Type != "text_regex" || mute.Then[0].Type != "mute" || mute.Then[1].Type != "increment_counter" {
		t.Fatalf("mute 规则错误: %+v", mute.When)
	}
	// kick 规则无计数
	kick := rules[6]
	if kick.Then[0].Type != "kick" || len(kick.Then) != 1 {
		t.Fatalf("kick 规则动作错误: %+v", kick.Then)
	}
	// 邀请规则存在
	hasInvite := false
	for _, r := range rules {
		if r.Name == "拒绝邀请" {
			hasInvite = true
		}
	}
	if !hasInvite {
		t.Fatal("缺少邀请审批规则")
	}
}

func TestMigrateV1FloodAndJoin(t *testing.T) {
	seq := 0
	rules := MigrateV1(nil, nil, &V1Flood{
		Enabled: true, WindowSeconds: 10, MaxMessages: 8, MuteMinutes: 10,
		KickOnRepeat: true, KickThreshold: 3,
	}, &V1JoinRequest{AutoApprove: true, Keyword: "中介", RejectText: "不允许"}, &seq)
	names := ruleNames(rules)
	// 默认豁免 + 同意邀请 + 拒绝邀请 + 刷屏升级 + 刷屏 + 拒绝中介 + 自动同意
	if len(rules) != 7 {
		t.Fatalf("规则数量错误: %v", names)
	}
	if names[3] != "刷屏多次移出" || names[4] != "刷屏检测" {
		t.Fatalf("刷屏规则顺序错误: %v", names)
	}
	fr := rules[4]
	cond, _ := DecodeRuleParams(true, fr.When[0].Type, fr.When[0].Params)
	fp := cond.(*FloodParams)
	if fp.WindowSec != 10 || fp.MaxCount != 8 {
		t.Fatalf("刷屏参数错误: %+v", fp)
	}
	muteP, _ := DecodeRuleParams(false, fr.Then[0].Type, fr.Then[0].Params)
	if muteP.(*MuteParams).Minutes != 10 {
		t.Fatal("刷屏禁言分钟错误")
	}
	if names[5] != "拒绝申请-中介" || names[6] != "自动同意申请" {
		t.Fatalf("审批规则错误: %v", names)
	}
}

func TestMigrateV1Welcome(t *testing.T) {
	seq := 0
	rules := MigrateV1(&V1Welcome{Enabled: true, Message: "欢迎 {nickname}"}, nil, nil, nil, &seq)
	if len(rules) != 4 {
		t.Fatalf("规则数量错误: %v", ruleNames(rules))
	}
	w := rules[3] // 豁免 + 邀请×2 之后
	if w.Name != "新人欢迎" || w.Event != EventGroupIncrease || len(w.When) != 0 {
		t.Fatalf("欢迎规则错误: %+v", w)
	}
	sp, _ := DecodeRuleParams(false, w.Then[0].Type, w.Then[0].Params)
	p := sp.(*SendMessageParams)
	if p.Message != "欢迎 {nickname}" || !p.At {
		t.Fatalf("欢迎参数错误: %+v", p)
	}
}

func TestMigrateV1DisabledModules(t *testing.T) {
	seq := 0
	rules := MigrateV1(nil, &V1Keyword{Enabled: false}, &V1Flood{Enabled: false}, nil, &seq)
	if len(rules) != 3 {
		t.Fatalf("禁用模块只应生成默认规则: %v", ruleNames(rules))
	}
}

func ruleNames(rs []Rule) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return out
}

// ---- 新条件/动作（扩展批次） ----

func TestTextLengthCondition(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "长文本", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "text_length", Params: mustJSON(TextLengthParams{Min: 20})}},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "太长"})}},
		Break: true,
	}})
	e.Process(msgCtx("短", "member"))
	if len(m.warns) != 0 {
		t.Fatal("短文本不应命中")
	}
	e.Process(msgCtx("这是一段超过二十个字符的很长的文本内容用于测试长度条件", "member"))
	if len(m.warns) != 1 {
		t.Fatal("长文本应命中")
	}
}

func TestURLCountCondition(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "广告链接", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "url_count", Params: mustJSON(CountThresholdParams{MinCount: 2})}},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "广告"})}},
		Break: true,
	}})
	e.Process(msgCtx("看 http://a.com 和 https://b.com/x", "member"))
	if len(m.warns) != 1 {
		t.Fatal("2 个链接应命中")
	}
	e.Process(msgCtx("只有一个 http://a.com", "member"))
	if len(m.warns) != 1 {
		t.Fatal("1 个链接不应命中")
	}
}

func TestImageCountCondition(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "图片轰炸", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "image_count", Params: mustJSON(CountThresholdParams{MinCount: 3})}},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "禁图"})}},
		Break: true,
	}})
	ctx := msgCtx("图", "member")
	ctx.MsgCounts = map[string]int{"image": 4}
	e.Process(ctx)
	if len(m.warns) != 1 {
		t.Fatal("4 张图应命中")
	}
	ctx = msgCtx("图", "member")
	ctx.MsgCounts = map[string]int{"image": 1}
	e.Process(ctx)
	if len(m.warns) != 1 {
		t.Fatal("1 张图不应命中")
	}
}

func TestAtCountAndMentionSelf(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "@轰炸", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "at_count", Params: mustJSON(CountThresholdParams{MinCount: 3})}},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "禁@多人"})}},
		Break: true,
	}, {
		ID: "r2", Name: "@机器人", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "mention_self"}},
		Then: []Action{{Type: "send_message", Params: mustJSON(SendMessageParams{Message: "我在呢"})}},
		Break: true,
	}})
	ctx := msgCtx("大家好", "member")
	ctx.AtTargets = []int64{1, 2, 3}
	e.Process(ctx)
	if len(m.warns) != 1 {
		t.Fatal("@3 人应命中")
	}
	ctx = msgCtx("机器人", "member")
	ctx.AtTargets = []int64{100}
	ctx.SelfID = 100
	e.Process(ctx)
	if len(m.sends) != 1 || !strings.Contains(m.sends[0], "我在呢") {
		t.Fatalf("@机器人应回复: %v", m.sends)
	}
}

func TestWeekdayCondition(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "周末严管", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "weekday", Params: mustJSON(WeekdayParams{Days: []int{0, 6}})}},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "周末"})}},
		Break: true,
	}})
	// 2026-08-08 是周六
	sat := time.Date(2026, 8, 8, 12, 0, 0, 0, time.Local)
	ctx := msgCtx("x", "member")
	ctx.Time = sat
	e.Process(ctx)
	if len(m.warns) != 1 {
		t.Fatal("周六应命中")
	}
	// 2026-08-10 是周一
	mon := time.Date(2026, 8, 10, 12, 0, 0, 0, time.Local)
	ctx = msgCtx("x", "member")
	ctx.Time = mon
	e.Process(ctx)
	if len(m.warns) != 1 {
		t.Fatal("周一不应命中")
	}
}

func TestProbabilityCondition(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "随机", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "probability", Params: mustJSON(ProbabilityParams{Percent: 100})}},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "必中"})}},
		Break: true,
	}})
	e.Process(msgCtx("x", "member"))
	if len(m.warns) != 1 {
		t.Fatal("100% 概率应命中")
	}
}

func TestUserJoinedWithinCondition(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "新人保护", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "user_joined_within", Params: mustJSON(DaysParams{Days: 7})}},
		Then: []Action{{Type: "send_message", Params: mustJSON(SendMessageParams{Message: "新人免罚"})}},
		Break: true,
	}})
	now := time.Now()
	ctx := msgCtx("x", "member")
	jt := now.Add(-2 * 24 * time.Hour)
	ctx.JoinTime = func() *time.Time { return &jt }
	e.Process(ctx)
	if len(m.sends) != 1 {
		t.Fatal("入群 2 天应命中（7 天内）")
	}
	old := now.Add(-30 * 24 * time.Hour)
	ctx = msgCtx("x", "member")
	ctx.JoinTime = func() *time.Time { return &old }
	e.Process(ctx)
	if len(m.sends) != 1 {
		t.Fatal("入群 30 天不应命中")
	}
	// 获取不到入群时间：不命中
	ctx = msgCtx("x", "member")
	ctx.JoinTime = func() *time.Time { return nil }
	e.Process(ctx)
	if len(m.sends) != 1 {
		t.Fatal("无入群时间不应命中")
	}
}

func TestRequestCommentEmpty(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "空备注拒绝", Enabled: true, Event: EventGroupRequest,
		When: []Condition{{Type: "request_comment_empty"}},
		Then: []Action{{Type: "reject_join", Params: mustJSON(RejectJoinParams{Reason: "请填写申请理由"})}},
		Break: true,
	}})
	req := &EventContext{Event: EventGroupRequest, GroupID: 100, UserID: 1, Text: "", SubType: "add", Flag: "f"}
	e.Process(req)
	if len(m.rejc) != 1 {
		t.Fatal("空备注应拒绝")
	}
	req = &EventContext{Event: EventGroupRequest, GroupID: 100, UserID: 2, Text: "想加入学习", SubType: "add", Flag: "f"}
	e.Process(req)
	if len(m.rejc) != 1 {
		t.Fatal("有备注不应拒绝")
	}
}

func TestInviterIs(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "指定邀请人欢迎", Enabled: true, Event: EventGroupIncrease,
		When: []Condition{{Type: "inviter_is", Params: mustJSON(InviterParams{UserID: 555})}},
		Then: []Action{{Type: "send_message", Params: mustJSON(SendMessageParams{Message: "欢迎熟人"})}},
		Break: true,
	}})
	ctx := &EventContext{Event: EventGroupIncrease, GroupID: 100, UserID: 9, OperatorID: 555, Time: time.Now()}
	e.Process(ctx)
	if len(m.sends) != 1 {
		t.Fatal("指定邀请人应命中")
	}
	ctx = &EventContext{Event: EventGroupIncrease, GroupID: 100, UserID: 9, OperatorID: 666, Time: time.Now()}
	e.Process(ctx)
	if len(m.sends) != 1 {
		t.Fatal("其他邀请人不命中")
	}
}

func TestSendPrivateAction(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "私聊提醒", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})}},
		Then: []Action{{Type: "send_private", Params: mustJSON(SendPrivateParams{Message: "请勿发广告"})}},
		Break: true,
	}})
	e.Process(msgCtx("广告", "member"))
	if len(m.sends) != 1 || !strings.Contains(m.sends[0], "请勿发广告") {
		t.Fatalf("私聊未发送: %v", m.sends)
	}
}

func TestWholeBanAction(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "宵禁", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "time_between", Params: mustJSON(TimeBetweenParams{Start: "02:00", End: "02:00"})}},
		Then: []Action{{Type: "whole_ban", Params: mustJSON(WholeBanParams{Enable: true})}},
		Break: true,
	}})
	ctx := msgCtx("x", "member")
	ctx.Time = time.Now()
	e.Process(ctx)
	if len(m.bans) != 1 || !strings.Contains(m.bans[0], "true") {
		t.Fatalf("全员禁言未执行: %v", m.bans)
	}
}

func TestCardAction(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "新人改名", Enabled: true, Event: EventMessage,
		When: []Condition{{Type: "user_joined_within", Params: mustJSON(DaysParams{Days: 3})}},
		Then: []Action{{Type: "card", Params: mustJSON(CardParams{Card: "新人-{nickname}"})}},
		Break: true,
	}})
	now := time.Now()
	jt := now.Add(-time.Hour)
	ctx := msgCtx("x", "member")
	ctx.JoinTime = func() *time.Time { return &jt }
	ctx.Nickname = func() string { return "小明" }
	e.Process(ctx)
	if len(m.cards) != 1 || !strings.Contains(m.cards[0], "新人-小明") {
		t.Fatalf("名片未设置: %v", m.cards)
	}
}

func TestDecrementCounter(t *testing.T) {
	s := newCounterStore()
	s.Increment(1, "kw", 100, 3, time.Hour)
	s.Decrement(1, "kw", 100, 1)
	if got := s.Get(1, "kw", 100, time.Hour); got != 2 {
		t.Fatalf("减一后应为 2: %d", got)
	}
	s.Decrement(1, "kw", 100, 10)
	if got := s.Get(1, "kw", 100, time.Hour); got != 0 {
		t.Fatalf("不低于 0: %d", got)
	}
}

// ---- 条件组合模式（when_mode：all/any + negate） ----

func TestWhenModeAny(t *testing.T) {
	// 任一满足（OR）：关键词 A 或 关键词 B 命中即警告
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "A或B", Enabled: true, Event: EventMessage, WhenMode: WhenModeAny,
		When: []Condition{
			{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})},
			{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "诈骗"})},
		},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "违规"})}},
		Break: true,
	}})
	e.Process(msgCtx("这里有广告", "member"))
	if len(m.warns) != 1 {
		t.Fatal("OR 模式：命中 A 应触发")
	}
	e.Process(msgCtx("警惕诈骗", "member"))
	if len(m.warns) != 2 {
		t.Fatal("OR 模式：命中 B 应触发")
	}
	e.Process(msgCtx("正常聊天", "member"))
	if len(m.warns) != 2 {
		t.Fatal("OR 模式：都未命中不应触发")
	}
}

func TestWhenAllWithNegate(t *testing.T) {
	// AND + 取反：非白名单 且 命中广告 → 警告（默认豁免规则的精细写法）
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "非白名单广告", Enabled: true, Event: EventMessage,
		When: []Condition{
			{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})},
			{Type: "in_whitelist", Negate: true},
		},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "x"})}},
		Break: true,
	}})
	ctx := msgCtx("广告", "member")
	ctx.IsWhitelisted = func() bool { return true }
	e.Process(ctx)
	if len(m.warns) != 0 {
		t.Fatal("白名单用户（取反不满足）不应命中")
	}
	ctx = msgCtx("广告", "member")
	ctx.IsWhitelisted = func() bool { return false }
	e.Process(ctx)
	if len(m.warns) != 1 {
		t.Fatal("非白名单用户应命中")
	}
}

func TestWhenModeDefaultIsAll(t *testing.T) {
	// 缺省 when_mode = all（向后兼容）
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "双条件", Enabled: true, Event: EventMessage,
		When: []Condition{
			{Type: "text_contains", Params: mustJSON(TextContainsParams{Text: "广告"})},
			{Type: "url_count", Params: mustJSON(CountThresholdParams{MinCount: 1})},
		},
		Then: []Action{{Type: "warn", Params: mustJSON(WarnParams{Reason: "x"})}},
		Break: true,
	}})
	e.Process(msgCtx("广告", "member"))
	if len(m.warns) != 0 {
		t.Fatal("AND 模式：只命中一个条件不应触发")
	}
	e.Process(msgCtx("广告 http://x.com", "member"))
	if len(m.warns) != 1 {
		t.Fatal("AND 模式：两个条件都命中应触发")
	}
}

func TestValidateWhenMode(t *testing.T) {
	r := Rule{ID: "r1", Name: "x", Enabled: true, Event: EventMessage,
		WhenMode: "maybe", Then: []Action{{Type: "noop"}}, Break: true}
	if err := ValidateRules([]Rule{r}, "rules"); err == nil {
		t.Fatal("非法 when_mode 应报错")
	}
	r.WhenMode = WhenModeAny
	if err := ValidateRules([]Rule{r}, "rules"); err != nil {
		t.Fatalf("合法 when_mode 不应报错: %v", err)
	}
}

// 名片动作支持"新人入群"事件（入群自动改名）。
func TestCardOnGroupIncrease(t *testing.T) {
	e, m := newTestEngine(t, []Rule{{
		ID: "r1", Name: "新人改名", Enabled: true, Event: EventGroupIncrease,
		Then: []Action{{Type: "card", Params: mustJSON(CardParams{Card: "新人-{nickname}"})}},
		Break: true,
	}})
	ctx := &EventContext{Event: EventGroupIncrease, GroupID: 100, UserID: 9, Time: time.Now()}
	ctx.Nickname = func() string { return "小红" }
	e.Process(ctx)
	if len(m.cards) != 1 || !strings.Contains(m.cards[0], "新人-小红") {
		t.Fatalf("入群改名未执行: %v", m.cards)
	}
}
