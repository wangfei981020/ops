package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Microsoft Teams 通知（Teams「工作流」/ Power Automate 的 Webhook）。
//
// 与日志告警、桌台维护告警两边的 Teams 渠道同一套判断：
//
//   - 地址只认 *.environment.api.powerplatform.com 的 /powerautomate/ 路径，必须带 sig
//     （sig 就是凭据）。端口只允许不写或 443 —— 工作流生成的地址本来就显式带 :443，
//     日志告警曾因为拒绝端口误拒过真实地址。
//   - 🔴 200 和 202 都算成功。工作流先回 202 再在 Power Automate 里发卡片：
//     这里的「成功」只代表**工作流收下了**，被限流或执行失败发送方看不到，只能去
//     Power Automate 运行历史查。所以投递记录上要写明是 Teams，别和飞书的「已送达」混为一谈。
//   - 错误信息里不出现地址：net/http 的连接错误会把整条 URL（含 sig）带出来。

const (
	teamsHostSuffix = ".environment.api.powerplatform.com"
	teamsPathPrefix = "/powerautomate/"
	// teamsByteBudget Teams 单条消息约 28KB，超了整条发不出去。镜像清单已按飞书 18KB
	// 预算分好片，Teams 卡片每行一个元素会更大一些，这里再兜一道。
	teamsByteBudget = 26 << 10
)

// TeamsDeliveredNote 记在投递记录上：Teams 的「已发送」只代表工作流已接收。
const TeamsDeliveredNote = "Teams 工作流已接收，送达以群内为准"

var teamsClient = &http.Client{
	Timeout: client.Timeout,
	// 地址是使用者填的：不跟随重定向
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// NormalizeTeamsWebhook 去掉地址里所有空白和不可见格式字符（零宽空格、BOM、NBSP …）。
// 从 Teams / Power Automate 页面复制地址常带进这些字符，真实 URL 里不会有。
func NormalizeTeamsWebhook(raw string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, raw)
}

// ValidateTeamsWebhook 校验 Teams 工作流地址（调用前先 Normalize）。每种不合规各报各的原因。
func ValidateTeamsWebhook(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("Teams Webhook 地址格式不正确")
	}
	if u.Scheme != "https" {
		return errors.New("Teams Webhook 地址必须是 https://")
	}
	if u.User != nil {
		return errors.New("Teams Webhook 地址不能带账号密码")
	}
	if p := u.Port(); p != "" && p != "443" {
		return fmt.Errorf("Teams Webhook 地址的端口只能是 443，当前是 %s", p)
	}
	host := strings.ToLower(u.Hostname())
	if prefix, ok := strings.CutSuffix(host, teamsHostSuffix); !ok || prefix == "" {
		return fmt.Errorf("Teams Webhook 地址只能是 *%s（Teams「工作流」生成的地址），当前主机是 %s", teamsHostSuffix, host)
	}
	if u.Fragment != "" {
		return errors.New("Teams Webhook 地址不能带 #")
	}
	if !strings.HasPrefix(u.Path, teamsPathPrefix) {
		return errors.New("Teams Webhook 地址路径必须以 /powerautomate/ 开头 —— 请在 Teams 工作流里复制完整的 HTTP POST 地址")
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == ".." || seg == "." {
			return errors.New("Teams Webhook 地址路径里不能有 . 或 ..")
		}
	}
	if u.Query().Get("sig") == "" {
		return errors.New("Teams Webhook 地址缺少 sig 参数 —— 请复制完整地址（包括 ? 后面的部分）")
	}
	return nil
}

// TeamsCard 同一份 Replication 的 Teams 版（Adaptive Card）。
//
// 内容与飞书卡片逐项一致（summaryLines / badBlock / okBlock / meta 是同一套），
// 只换外壳：飞书的 header 颜色 → 标题容器样式；lark_md → 一行一个 TextBlock
// （Teams 会把 TextBlock 里的单个换行吞掉）；Teams 不认行内代码，反引号去掉。
func TeamsCard(r Replication) []byte {
	failed := r.Level == LevelFailed
	style, icon, what := "good", "✅", "镜像同步完成"
	if failed {
		style, icon, what = "attention", "❌", "镜像同步失败"
	}
	title := fmt.Sprintf("%s %s · %s%s", icon, what, r.Policy, pageSuffix(r))

	var blocks []string
	if r.Page <= 1 {
		blocks = append(blocks, summaryLines(r))
	}
	if len(r.Bad) > 0 {
		blocks = append(blocks, badBlock(r, true))
	}
	if len(r.OK) > 0 {
		blocks = append(blocks, okBlock(r, true))
	}

	render := func(lines []string) []byte {
		items := []any{map[string]any{
			"type": "Container", "style": style, "bleed": true,
			"items": []any{map[string]any{
				"type": "TextBlock", "text": title, "wrap": true, "size": "Medium", "weight": "Bolder",
			}},
		}}
		for _, l := range lines {
			if l == "" { // 分区之间
				items = append(items, map[string]any{"type": "TextBlock", "text": " ", "spacing": "Small", "separator": true})
				continue
			}
			items = append(items, map[string]any{"type": "TextBlock", "text": l, "wrap": true, "spacing": "None"})
		}
		items = append(items, map[string]any{
			"type": "TextBlock", "text": meta(r), "wrap": true, "isSubtle": true, "size": "Small",
			"separator": true, "spacing": "Medium",
		})
		return marshalNoEscape(map[string]any{
			"type": "message",
			"attachments": []any{map[string]any{
				"contentType": "application/vnd.microsoft.card.adaptive", "contentUrl": nil,
				"content": map[string]any{
					"type": "AdaptiveCard", "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
					"version": "1.4", "msteams": map[string]any{"width": "Full"}, "body": items,
				},
			}},
		})
	}

	var lines []string
	for i, b := range blocks {
		if i > 0 {
			lines = append(lines, "")
		}
		for _, l := range strings.Split(strings.ReplaceAll(b, "`", ""), "\n") {
			lines = append(lines, l)
		}
	}
	out := render(lines)
	// 🔴 超预算就从尾部砍行并写明，而不是整条发不出去。失败清单在前面，砍的是成功清单的尾巴。
	for len(out) > teamsByteBudget && len(lines) > 1 {
		cut := len(lines) / 5
		if cut < 1 {
			cut = 1
		}
		lines = append(lines[:len(lines)-cut-1:len(lines)-cut-1],
			fmt.Sprintf("…（卡片过长，其余未列出，去「镜像同步」页按 Execution %d 查看）", r.ExecID))
		out = render(lines)
	}
	return out
}

// TeamsTestCard 渠道「测试」按钮用的卡片。
func TeamsTestCard(channel string) []byte {
	return marshalNoEscape(map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive", "contentUrl": nil,
			"content": map[string]any{
				"type": "AdaptiveCard", "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
				"version": "1.4", "msteams": map[string]any{"width": "Full"},
				"body": []any{
					map[string]any{"type": "TextBlock", "text": "【OpsVersion】测试消息", "weight": "Bolder", "size": "Medium", "wrap": true},
					map[string]any{"type": "TextBlock", "text": "通知渠道「" + channel + "」配置正常", "wrap": true},
				},
			},
		}},
	})
}

// SendTeams 投递一张 Teams 卡片。200 / 202 视为成功（见文件头）。
func SendTeams(webhook string, payload []byte) error {
	webhook = NormalizeTeamsWebhook(webhook)
	// 发之前再校验一次：库里的旧数据、手改过的数据，也不能让这里变成「往任意地址发请求」
	if err := ValidateTeamsWebhook(webhook); err != nil {
		return err
	}
	return postTeams(webhook, payload)
}

func postTeams(webhook string, payload []byte) error {
	resp, err := teamsClient.Post(webhook, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("发送 Teams 消息失败：%w", redactTeamsURL(err, webhook))
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return errors.New("被 Teams 限流（同一账号 5 分钟约 25 条），请稍后再试")
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return fmt.Errorf("Teams 返回 HTTP %d：地址里的 sig 不对，或工作流已关闭", resp.StatusCode)
	case resp.StatusCode == 404:
		return errors.New("Teams 返回 HTTP 404：工作流不存在，可能已被删除")
	default:
		return fmt.Errorf("Teams 返回 HTTP %d", resp.StatusCode)
	}
}

// redactTeamsURL 从错误信息里去掉整条地址和单独的 sig。
func redactTeamsURL(err error, raw string) error {
	msg := strings.ReplaceAll(err.Error(), raw, "<teams-webhook>")
	if u, perr := url.Parse(raw); perr == nil {
		if sig := u.Query().Get("sig"); sig != "" {
			msg = strings.ReplaceAll(msg, sig, "****")
			msg = strings.ReplaceAll(msg, url.QueryEscape(sig), "****")
		}
	}
	return errors.New(msg)
}

// marshalNoEscape json 序列化但不做 HTML 转义（< > & 原样，省卡片字节）。
func marshalNoEscape(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// ─────────────── 按渠道类型分派 ───────────────

const (
	KindLark  = "lark"
	KindTeams = "teams"
)

// ValidKind 渠道类型是否支持。
func ValidKind(k string) bool { return k == KindLark || k == KindTeams }

// Deliver 按渠道类型投递一次复制通知。两条链路（webhook 实时 / 采集器兜底）都走这里，
// 保证同一次复制无论从哪条路发出来、发到哪种群，内容是一样的。
func Deliver(kind, webhook string, r Replication) error {
	if kind == KindTeams {
		return SendTeams(webhook, TeamsCard(r))
	}
	return SendFeishuCard(webhook, Card(r))
}

// SendTest 渠道「测试」：按类型真发一条。
func SendTest(kind, webhook, channel string) error {
	if kind == KindTeams {
		return SendTeams(webhook, TeamsTestCard(channel))
	}
	return SendFeishu(webhook, "【OpsVersion】测试消息：通知渠道「"+channel+"」配置正常")
}

// ─────────────── 同步失败时 @ 人（只有 Teams） ───────────────
//
// 🔴 @ 不放进失败卡片本身，而是失败卡片发完之后**另发一条**小卡片：
// 工作流只回 202、不告诉你账号存不存在；账号填错时 Teams 静默丢掉这个人，
// 同一张卡片里其他人的名字还会跟着错位（日志告警那边实际出过）。
// 分开发，账号错了最多影响这条提醒，失败告警本身照样送达。

// TeamsMention 一个要 @ 的人。Account 是 Teams 登录账号（一般是公司邮箱）。
type TeamsMention struct {
	Name    string
	Account string
}

var teamsAccountRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._%+-]{0,63}@[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?)+$`)

// NormalizeTeamsAccount 去空白（含复制带进来的不可见字符）并转小写。
func NormalizeTeamsAccount(s string) string {
	return strings.ToLower(NormalizeTeamsWebhook(s))
}

// ValidTeamsAccount 账号格式像不像登录地址（不代表账号存在）。调用前先 Normalize。
func ValidTeamsAccount(s string) bool { return len(s) <= 254 && teamsAccountRe.MatchString(s) }

// ValidMentionName 卡片上 @ 显示的名字：不能带 < >（会破坏 <at> 标记）。
func ValidMentionName(s string) bool {
	return s != "" && len([]rune(s)) <= 64 && !strings.ContainsAny(s, "<>")
}

// TeamsMentionLog 写进投递记录的 @ 明细：有人说没被提醒时，对照用的是哪个账号。
func TeamsMentionLog(ms []TeamsMention) string {
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		parts = append(parts, fmt.Sprintf("%s(%s)", m.Name, m.Account))
	}
	return "@ " + strings.Join(parts, " ")
}

// TeamsMentionCard 同步失败后追发的 @ 提醒。
func TeamsMentionCard(r Replication, ms []TeamsMention) []byte {
	head := "‼️ 镜像同步失败，请查看上一条 · " + r.Policy
	if r.ExecID > 0 {
		head += fmt.Sprintf("（Execution %d）", r.ExecID)
	}
	return mentionCard(head, ms)
}

// TeamsMentionTestCard 渠道「测试」时追发的 @ 提醒，和真实提醒同一种卡片。
func TeamsMentionTestCard(channel string, ms []TeamsMention) []byte {
	return mentionCard("【OpsVersion】测试 @ 提醒 · 渠道「"+channel+"」：名字高亮 = 账号正确，黑字 = 账号不对", ms)
}

func mentionCard(head string, ms []TeamsMention) []byte {
	tags := make([]string, 0, len(ms))
	entities := make([]any, 0, len(ms))
	seen := map[string]bool{}
	for _, m := range ms {
		if m.Account == "" || seen[m.Account] {
			continue
		}
		seen[m.Account] = true
		tag := "<at>" + m.Name + "</at>"
		tags = append(tags, tag)
		entities = append(entities, map[string]any{
			"type": "mention", "text": tag,
			"mentioned": map[string]any{"id": m.Account, "name": m.Name},
		})
	}
	return marshalNoEscape(map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive", "contentUrl": nil,
			"content": map[string]any{
				"type": "AdaptiveCard", "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
				"version": "1.4", "msteams": map[string]any{"width": "Full", "entities": entities},
				"body": []any{
					map[string]any{"type": "TextBlock", "text": head, "wrap": true, "weight": "Bolder", "color": "Attention"},
					map[string]any{"type": "TextBlock", "text": strings.Join(tags, "  "), "wrap": true, "spacing": "Small"},
				},
			},
		}},
	})
}
