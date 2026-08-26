package export

import "testing"

// 🔴 页签名撞车：列标识加了项目名之后很容易超 31 字符，
// 两个项目的列截断后会同名 —— 不去重的话第二列的明细会盖掉第一列的。
func TestUniqueSheetNameDedup(t *testing.T) {
	used := map[string]bool{}
	a := uniqueSheetName(sheetName("很长很长的平台名字啊啊啊啊啊·项目一/UAT"), used)
	b := uniqueSheetName(sheetName("很长很长的平台名字啊啊啊啊啊·项目二/UAT"), used)
	if a == b {
		t.Fatalf("两列页签同名 %q —— 第二列的明细会盖掉第一列的", a)
	}
	for _, n := range []string{a, b} {
		if len([]rune(n)) > 31 {
			t.Errorf("页签名 %q 有 %d 字符，超过 Excel 的 31 上限，excelize 会直接报错", n, len([]rune(n)))
		}
	}
}

// 不撞车时不要平白加后缀 —— 名字要保持可读
func TestUniqueSheetNameKeepsOriginal(t *testing.T) {
	used := map[string]bool{}
	if got := uniqueSheetName("A平台-UAT", used); got != "A平台-UAT" {
		t.Errorf("没撞车却改了名：%q", got)
	}
}

// 连撞三次也要各不相同
func TestUniqueSheetNameThree(t *testing.T) {
	used := map[string]bool{}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		n := uniqueSheetName("同一个名字", used)
		if seen[n] {
			t.Fatalf("第 %d 次又撞了：%q", i+1, n)
		}
		seen[n] = true
	}
}
