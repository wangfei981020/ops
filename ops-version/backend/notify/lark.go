// Package notify 飞书（Lark）机器人 webhook 通知。
//
// ⚠️ 这份实现从 某个同类产品 原样搬来 —— 里面那条「HTTP 200 也可能是失败」的判定
// 是实测撞出来的，重写一遍必然漏掉。改动只有包注释和这段说明。
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var client = &http.Client{Timeout: 10 * time.Second}

// SendFeishu 发飞书自定义机器人文本消息。
//
// # 🔴 为什么必须检查响应内容
//
// 原来的实现只看 `client.Post` 有没有返回网络错误，然后就 `return nil`：
//
//	resp, err := client.Post(...)
//	if err != nil { return err }
//	return nil                      // ← 响应体、状态码，一概不看
//
// 于是**所有投递失败都被记成"已送达"**：
//
//   - webhook 地址无效、机器人被移出群、hook 被重置
//     → 飞书返回 **HTTP 200 + code≠0**，上面这段代码认为成功
//   - webhook 为空
//     → 直接 return nil，"根本没配"被当成"发送成功"
//
// 实测（2026-08-17，本地）：我填了一个假 webhook
// `https://open.feishu.cn/open-apis/bot/v2/hook/FAKE-SECRET-abc123xyz789`，
// 任务执行记录里 `notify_state` 显示 **sent**、群名显示「全局兜底出口」。
//
// ⚠️ 这比"提醒发不出去"严重得多：
// 前者界面上写着没发出去，人还会去查；
// 后者界面上写着**已送达**，于是没人会去查 ——
// 而域名到期、证书到期、磁盘告警全走这条路。
//
// 飞书的响应形状：
//
//	成功  {"code":0,"msg":"success","data":{}}
//	失败  {"code":19001,"msg":"param invalid"}  （HTTP 仍是 200）
//
// 所以判定必须三层都过：网络 → HTTP 状态码 → 业务 code。
func SendFeishu(webhook, text string) error {
	if webhook == "" {
		// ⚠️ 不能返回 nil。
		//
		//	"没有配置投递出口"和"消息已送达"是完全相反的两件事，
		//	返回 nil 会让调用方把 notify_state 记成 sent。
		//	调用方要跳过投递的话，应当自己判断出口为空（现在都是这么做的），
		//	而不是让这里假装成功。
		return fmt.Errorf("没有配置飞书 webhook，消息未发送")
	}
	body, _ := json.Marshal(map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	})
	resp, err := client.Post(webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 读全响应体：飞书把真正的失败原因放在这里，而不是状态码上
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("飞书返回 HTTP %d：%.200s", resp.StatusCode, raw)
	}
	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		// 解析不了响应就**不能断言成功**。
		//
		//	能走到这里说明拿到了 200，但内容不是飞书的标准响应 ——
		//	多半是 webhook 填成了别的地址（比如一个网页），
		//	而那种情况下消息肯定没进群。
		return fmt.Errorf("飞书响应不是预期的 JSON（webhook 地址可能填错了）：%.200s", raw)
	}
	if r.Code != 0 {
		// code 非 0 的常见原因：hook 被重置、机器人被移出群、地址里的 token 错了
		return fmt.Errorf("飞书拒绝了这条消息：code=%d msg=%s", r.Code, r.Msg)
	}
	return nil
}
