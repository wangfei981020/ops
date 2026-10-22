package store

import (
	"strconv"
	"strings"
	"testing"
)

func TestNormPlanServices(t *testing.T) {
	got, err := normPlanServices([]string{" app-a ", "", "app-b", "app-a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "app-a,app-b" {
		t.Fatalf("去空白去重失败: %v", got)
	}

	// nil 进来也要给空数组，存进库是 "[]" 而不是 "null"
	got, err = normPlanServices(nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("nil 应得到空数组: %v %v", got, err)
	}

	if _, err := normPlanServices([]string{strings.Repeat("x", maxPlanServiceLen+1)}); err == nil {
		t.Fatal("超长服务名应被拒绝")
	}

	many := make([]string, maxPlanServices+1)
	for i := range many {
		many[i] = "svc-" + strconv.Itoa(i)
	}
	if _, err := normPlanServices(many); err == nil {
		t.Fatal("超过数量上限应被拒绝")
	}
}
