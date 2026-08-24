package api

import "testing"

// 上传校验：只认白名单类型，超限拒绝，坏 base64 拒绝。
func TestValidateImage(t *testing.T) {
	// 1KB 的合法 PNG data URI（内容是什么不重要，能 base64 解开即可）
	small := "data:image/png;base64," + b64(1024)
	cases := []struct {
		name, in string
		max      int
		wantErr  bool
	}{
		{"空串合法（表示清空）", "", 1024, false},
		{"正常 PNG", small, 4096, false},
		{"SVG 允许", "data:image/svg+xml;base64," + b64(64), 4096, false},
		// 🔴 data:text/html 会在 <img> 之外被当成脚本执行 —— 必须拒绝
		{"HTML data URI 必须拒绝", "data:text/html;base64," + b64(64), 4096, true},
		{"裸 URL 拒绝", "https://example.com/a.png", 4096, true},
		{"超出大小拒绝", small, 512, true},
		{"坏 base64 拒绝", "data:image/png;base64,!!!not-base64!!!", 4096, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := validateImage(c.in, c.max, "Logo")
			if (msg != "") != c.wantErr {
				t.Errorf("validateImage → %q，期望出错=%v", msg, c.wantErr)
			}
		})
	}
}

// b64 造一个能解码的 base64 串，解出来约 n 字节
func b64(n int) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	cnt := (n + 2) / 3 * 4
	out := make([]byte, cnt)
	for i := range out {
		out[i] = tbl[i%64]
	}
	// 末尾补成合法长度
	for i := 0; i < cnt%4; i++ {
		out[len(out)-1-i] = '='
	}
	return string(out)
}
