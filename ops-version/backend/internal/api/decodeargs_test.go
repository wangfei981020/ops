package api

import (
	"strings"
	"testing"
)

// 🔴 参数类型不对必须**报错**，不能静默忽略。
//
// 实测过（2026-08-21）：MCP 客户端把 limit 传成字符串 "1"，
// 而原来的 `_ = json.Unmarshal(raw, &p)` 把错误丢了 ——
// Go 的行为是"类型不匹配的那个字段保持零值，其余照常解析成功"，
// 于是 columns 和 only_diff 都生效、唯独 limit 变成 0 走了默认上限 200，
// **AI 传 limit=1 拿回 71 行**。
//
// ⚠️ 这类"参数被静默忽略"比直接报错危险得多：
// 报错了调用方会改，静默忽略则是它以为自己已经限制了，然后拿着全量往下走。
func TestDecodeArgsRejectsWrongType(t *testing.T) {
	var p struct {
		Columns []struct{ Org, Env string } `json:"columns"`
		OnlyDif *bool                       `json:"only_diff"`
		Limit   int                         `json:"limit"`
	}
	err := decodeArgs([]byte(`{"columns":[{"org":"我方","env":"UAT"}],"only_diff":true,"limit":"1"}`), &p)
	if err == nil {
		t.Fatal("limit 传了字符串却没报错 —— 这正是生产上那个「传了 limit 却不生效」的 bug")
	}
	// 报错要说清楚是**哪个参数**，否则调用方得一个个试
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("错误信息里没点名是哪个参数：%v", err)
	}
	// 也要说清楚**该传什么**
	if !strings.Contains(err.Error(), "int") {
		t.Errorf("错误信息里没说需要什么类型：%v", err)
	}
}

func TestDecodeArgsAcceptsCorrectType(t *testing.T) {
	var p struct {
		Limit int `json:"limit"`
	}
	if err := decodeArgs([]byte(`{"limit":7}`), &p); err != nil {
		t.Fatalf("合法参数不该报错：%v", err)
	}
	if p.Limit != 7 {
		t.Errorf("limit = %d，要 7", p.Limit)
	}
}

// 空参数是合法的 —— 好几个工具的参数全是可选的
func TestDecodeArgsEmpty(t *testing.T) {
	var p struct {
		Limit int `json:"limit"`
	}
	if err := decodeArgs(nil, &p); err != nil {
		t.Errorf("空参数不该报错：%v", err)
	}
	if err := decodeArgs([]byte(`{}`), &p); err != nil {
		t.Errorf("空对象不该报错：%v", err)
	}
}
