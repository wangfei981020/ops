package store

import "testing"

func TestJoinLines(t *testing.T) {
	cases := []struct {
		name, want string
		in         []string
	}{
		{"正常多行", "a\nb", []string{"a", "b"}},
		// 🔴 空行会变成一条 "" 规则匹配所有 ns —— 采集范围悄悄扩到整个集群，
		//    错了不报错、只是结果变宽，最难发现
		{"丢空行", "a\nb", []string{"a", "", "  ", "b"}},
		{"去首尾空白", "a\nb", []string{" a ", "b\t"}},
		{"逗号也当分隔符", "app-uat\napp-prod", []string{"app-uat,app-prod"}},
		{"混合换行与逗号", "a\nb\nc", []string{"a", "b,c"}},
		{"去重", "a\nb", []string{"a", "b", "a"}},
		{"全空", "", []string{"", "  "}},
		{"nil", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := joinLines(c.in); got != c.want {
				t.Errorf("joinLines(%q) = %q, 要 %q", c.in, got, c.want)
			}
		})
	}
}
