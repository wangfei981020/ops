package imageref

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in        string
		key       string
		tag       string
		registry  string
		project   string
		build     int // -1 = nil
		versioned bool
	}{
		// 生产真实串
		{"harbor.example.com/appA/wallet-client-backend:20260519082034-58ac8c3-114",
			"wallet-client-backend", "20260519082034-58ac8c3-114", "harbor.example.com", "appA", 114, true},
		// 客户侧：registry 和项目名都不同，key 必须相同
		{"harbor.b-corp.com/xyz-platform/wallet-client-backend:20260519082034-58ac8c3-114",
			"wallet-client-backend", "20260519082034-58ac8c3-114", "harbor.b-corp.com", "xyz-platform", 114, true},
		// 前端镜像：tag 只有 时间戳-构建号，没有 commit 段
		{"dev-harbor.slleisure.com/app-test/atmosphere-frontend:20260730083740-100",
			"atmosphere-frontend", "20260730083740-100", "dev-harbor.slleisure.com", "app-test", 100, true},
		// 🔴 registry 带端口：冒号有两个，按第一个切会全错
		{"registry.example.com/ops/app-backend:v0.92.5",
			"app-backend", "v0.92.5", "registry.example.com", "ops", -1, true},
		// 🔴 Harbor 返回的尾巴必须剥掉
		{"harbor.example.com/appA/bi-task-backend:20260812090855-08e4b67-39 [3 item(s) in total]",
			"bi-task-backend", "20260812090855-08e4b67-39", "harbor.example.com", "appA", 39, true},
		// 🔴 非版本化 tag：必须标 unversioned，不能因两边相同就判绿
		{"nginx:stable-otel", "nginx", "stable-otel", "", "", -1, false},
		{"nginx:latest", "nginx", "latest", "", "", -1, false},
		// 隐式 docker.io，第一段是项目不是 registry
		{"nginxinc/nginx-unprivileged:1.27-alpine",
			"nginx-unprivileged", "1.27-alpine", "", "nginxinc", -1, true},
		// pod 的 imageID 形态
		{"registry.example.com/ops/app-backend@sha256:949b8b92efce",
			"app-backend", "", "registry.example.com", "ops", -1, false},
		// 多级项目路径
		{"registry.k8s.io/metrics-server/metrics-server:v0.9.0",
			"metrics-server", "v0.9.0", "registry.k8s.io", "metrics-server", -1, true},
	}
	for _, c := range cases {
		got := Parse(c.in)
		if got.Name != c.key || got.Tag != c.tag || got.Registry != c.registry || got.Project != c.project {
			t.Errorf("Parse(%q)\n got: name=%q tag=%q reg=%q proj=%q\nwant: name=%q tag=%q reg=%q proj=%q",
				c.in, got.Name, got.Tag, got.Registry, got.Project, c.key, c.tag, c.registry, c.project)
		}
		b := -1
		if got.BuildNo != nil {
			b = *got.BuildNo
		}
		if b != c.build {
			t.Errorf("Parse(%q) buildNo = %d, want %d", c.in, b, c.build)
		}
		if got.IsVersioned != c.versioned {
			t.Errorf("Parse(%q) versioned = %v, want %v", c.in, got.IsVersioned, c.versioned)
		}
	}
}

// 同一个服务在三家的串完全不同，key 必须一致 —— 这是整个对账成立的前提
func TestKeyStableAcrossOrgs(t *testing.T) {
	imgs := []string{
		"harbor.example.com/appA/wallet-client-backend:20260519082034-58ac8c3-114",
		"harbor.a-corp.com/appA/wallet-client-backend:20260421031122-a71bd04-109",
		"harbor.b-corp.com/xyz-platform/wallet-client-backend:20260519082034-58ac8c3-114",
		"harbor.c-corp.com/app-cn/wallet-client-backend:20260519082034-58ac8c3-114 [1 item(s) in total]",
	}
	want := "wallet-client-backend"
	for _, im := range imgs {
		if k := Parse(im).Key(nil); k != want {
			t.Errorf("Parse(%q).Key() = %q, want %q", im, k, want)
		}
	}
}
