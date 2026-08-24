package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 🔴 按**字符**截断，不按字节 —— 按字节切会把中文切成非法 UTF-8，
// MySQL 拒收整行（Error 1366），于是**整条状态都写不进库**。
func TestClipText(t *testing.T) {
	cases := []struct {
		name, in       string
		max, wantRunes int
	}{
		{"英文不截", "hello", 500, 5},
		{"中文不截", "权限不足", 500, 4},
		{"英文截断", strings.Repeat("a", 600), 500, 500},
		// 600 个中文 = 1800 字节，按字节切必然切在字符中间
		{"中文截断", strings.Repeat("权", 600), 500, 500},
		{"中英混合", strings.Repeat("权限a", 300), 500, 500},
		{"空串", "", 500, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clipText(c.in, c.max)
			if !utf8.ValidString(got) {
				t.Fatalf("截出了非法 UTF-8 —— MySQL 会整行拒收：%q", got)
			}
			if n := utf8.RuneCountInString(got); n != c.wantRunes {
				t.Errorf("字符数 = %d，要 %d", n, c.wantRunes)
			}
		})
	}
}

// 复刻生产上那条真实的错误文案：带中文、超长、且截断点恰好落在汉字中间
func TestClipTextOnRealErrorMessage(t *testing.T) {
	msg := "集群 local: 权限不足: Rancher 拒绝了这次请求：读整个集群的工作负载需要**集群级**权限，" +
		"而这个账号多半只有 project / namespace 级的只读授权（Rancher 原话里写着 at the cluster scope，" +
		"就是这个意思）。⚠️ 不必去放大权限 —— 在本环境的「ns 包含」里填上要采集的命名空间（一行一个），" +
		"我们就会按 namespace 逐个读取，project 级授权就够了。（自动展开需要账号能列出 namespace 列表，" +
		"你这个账号读不到，所以要手填。）"
	got := clipText(msg, 500)
	if !utf8.ValidString(got) {
		t.Fatalf("生产那条文案截出了非法 UTF-8：%q", got[len(got)-10:])
	}
	if utf8.RuneCountInString(got) > 500 {
		t.Errorf("超过 500 字符：%d", utf8.RuneCountInString(got))
	}
}
