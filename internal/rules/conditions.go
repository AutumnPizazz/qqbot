package rules

import (
	"math/rand"
	"regexp"
	"strings"
	"time"
)

// ---- 条件评估实现（参数已由 validateConditionParams 解析校验） ----

func evalAlways(_ *Engine, ctx *EventContext, _ any) (bool, error) {
	return true, nil
}

func evalIsExempt(_ *Engine, ctx *EventContext, _ any) (bool, error) {
	if ctx.IsOwner {
		return true, nil
	}
	if ctx.Role == "owner" || ctx.Role == "admin" {
		return true, nil
	}
	if ctx.IsWhitelisted != nil && ctx.IsWhitelisted() {
		return true, nil
	}
	return false, nil
}

func evalIsOwner(_ *Engine, ctx *EventContext, _ any) (bool, error) {
	return ctx.IsOwner, nil
}

func evalUserID(_ *Engine, ctx *EventContext, p any) (bool, error) {
	return ctx.UserID == p.(*UserIDParams).UserID, nil
}

func evalUserIDIn(_ *Engine, ctx *EventContext, p any) (bool, error) {
	for _, id := range p.(*UserIDsParams).UserIDs {
		if id == ctx.UserID {
			return true, nil
		}
	}
	return false, nil
}

func evalUserRole(_ *Engine, ctx *EventContext, p any) (bool, error) {
	for _, r := range p.(*UserRoleParams).Roles {
		if r == ctx.Role {
			return true, nil
		}
	}
	return false, nil
}

func evalInWhitelist(_ *Engine, ctx *EventContext, _ any) (bool, error) {
	if ctx.IsWhitelisted == nil {
		return false, nil
	}
	return ctx.IsWhitelisted(), nil
}

func evalTextContains(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*TextContainsParams)
	return strings.Contains(strings.ToLower(ctx.Text), strings.ToLower(pp.Text)), nil
}

func evalTextRegex(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*TextRegexParams)
	return pp.re.MatchString(ctx.Text), nil
}

func evalRequestContains(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*TextContainsParams)
	return strings.Contains(strings.ToLower(ctx.Text), strings.ToLower(pp.Text)), nil
}

func evalRequestRegex(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*TextRegexParams)
	return pp.re.MatchString(ctx.Text), nil
}

func evalTextRepeat(e *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*TextRepeatParams)
	n := e.recent.countSame(ctx.GroupID, ctx.UserID, ctx.Text, time.Duration(pp.WindowSec)*time.Second, true)
	return n >= pp.MinCount, nil
}

func evalMessageHasType(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*MessageHasTypeParams)
	for _, t := range pp.Types {
		for _, got := range ctx.MsgTypes {
			if t == got {
				return true, nil
			}
		}
	}
	return false, nil
}

func evalFlood(e *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*FloodParams)
	n := e.floods.count(ctx.GroupID, ctx.UserID, time.Duration(pp.WindowSec)*time.Second)
	return n >= pp.MaxCount, nil
}

func evalStrikeCount(e *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*StrikeCountParams)
	n := e.counters.Get(ctx.GroupID, pp.CounterID, ctx.UserID, time.Duration(pp.WindowHours)*time.Hour)
	return n >= pp.MinCount, nil
}

func evalTimeBetween(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*TimeBetweenParams)
	return inTimeRange(ctx.Time, pp.Start, pp.End)
}

func evalSubtypeIs(_ *Engine, ctx *EventContext, p any) (bool, error) {
	return ctx.SubType == p.(*SubtypeParams).Subtype, nil
}

func evalTextLength(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*TextLengthParams)
	n := len([]rune(ctx.Text))
	if pp.Min > 0 && n < pp.Min {
		return false, nil
	}
	if pp.Max > 0 && n > pp.Max {
		return false, nil
	}
	return true, nil
}

// urlRe 提取消息文本中的链接。
var urlRe = regexp.MustCompile(`https?://[^\s]+`)

func evalURLCount(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*CountThresholdParams)
	return len(urlRe.FindAllString(ctx.Text, -1)) >= pp.MinCount, nil
}

func evalImageCount(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*CountThresholdParams)
	return ctx.MsgCounts["image"] >= pp.MinCount, nil
}

func evalAtCount(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*CountThresholdParams)
	return len(ctx.AtTargets) >= pp.MinCount, nil
}

func evalMentionSelf(_ *Engine, ctx *EventContext, _ any) (bool, error) {
	for _, uid := range ctx.AtTargets {
		if uid == ctx.SelfID {
			return true, nil
		}
	}
	return false, nil
}

func evalWeekday(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*WeekdayParams)
	wd := int(ctx.Time.Weekday()) // Go: Sunday=0
	for _, d := range pp.Days {
		if d == wd {
			return true, nil
		}
	}
	return false, nil
}

func evalProbability(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*ProbabilityParams)
	return rand.Intn(100) < pp.Percent, nil
}

func evalUserJoinedWithin(_ *Engine, ctx *EventContext, p any) (bool, error) {
	pp := p.(*DaysParams)
	if ctx.JoinTime == nil {
		return false, nil
	}
	jt := ctx.JoinTime()
	if jt == nil || jt.IsZero() {
		return false, nil // 获取不到入群时间（连接不可用等）→ 不满足
	}
	return time.Since(*jt) <= time.Duration(pp.Days)*24*time.Hour, nil
}

func evalRequestCommentEmpty(_ *Engine, ctx *EventContext, _ any) (bool, error) {
	return strings.TrimSpace(ctx.Text) == "", nil
}

func evalInviterIs(_ *Engine, ctx *EventContext, p any) (bool, error) {
	return ctx.OperatorID != 0 && ctx.OperatorID == p.(*InviterParams).UserID, nil
}

// 正则参数在解析后编译缓存（避免每事件重编译）。
func compileRegexParams(p *TextRegexParams) error {
	re, err := regexp.Compile(p.Pattern)
	if err != nil {
		return err
	}
	p.re = re
	return nil
}
