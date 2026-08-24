// Package imageref 把一个镜像串拆成对账需要的几个部分。
//
// 这是整个对账的地基：key 取错了，后面所有判定都是错的，而且错得很隐蔽
// （表面上是「对方少了一个服务 + 多了一个服务」，看着像业务差异，实则是解析 bug）。
//
// 为什么 key 是「镜像名最后一段」而不是别的：
//
//	harbor.example.com / appA          / wallet-client-backend : 20260519082034-58ac8c3-114
//	harbor.b-corp.com     / xyz-platform / wallet-client-backend : 20260519082034-58ac8c3-114
//	└──── registry ────┘   └─ 项目名 ─┘   └────── key ──────┘   └────── 版本 ──────┘
//	        双方不同           双方可能不同
//
// registry host 各家不同；Harbor 项目名客户那边可能跟我方不一致；
// namespace 和 workload 名是对方 k8s 里随便起的，我们控制不了。
// 双方唯一都能保证一致的只有**服务名和版本号**，所以 key 只能定在这里。
package imageref

import (
	"regexp"
	"strconv"
	"strings"
)

// harborListSuffix 匹配 Harbor 返回的镜像名尾巴，形如 ` [3 item(s) in total]`。
//
// ⚠️ 这个尾巴查文档查不到，只有真跑过 replication task 接口才知道。
// 不剥掉的话同一个服务会被算成两个 —— 一个带尾巴一个不带，
// 表现为「对方多了一个服务、我方少了一个服务」，最容易被误判成业务差异。
var harborListSuffix = regexp.MustCompile(`\s*\[\d+\s+item\(s\)\s+in\s+total\]\s*$`)

// nonVersionTags 非版本化 tag。
//
// 这类 tag 指向的内容随时会变，两边字符串相同**不代表跑的是同一个镜像**。
// 必须归入「无法判定」，不能判绿 —— 判绿等于用一个假的一致性掩盖真实风险。
var nonVersionTags = map[string]bool{
	"latest": true, "stable": true, "main": true, "master": true,
	"dev": true, "prod": true, "release": true, "edge": true, "": true,
}

// Ref 是一个镜像串解析后的结果。
type Ref struct {
	Raw      string // 原始串（清洗尾巴后）
	Registry string // registry host，可能带端口。不参与比对
	Project  string // 项目/命名路径，可能多级。不参与比对
	Name     string // 镜像名最后一段 —— **这就是对账 key**
	Tag      string // 版本
	Digest   string // 形如 sha256:xxx，来自 pod 的 imageID；deployment 里没有

	// BuildNo 从 tag 末段解析出的构建号，解析不出为 nil。
	// 为 nil 时只能判「相同 / 不同」，**不能算落后几个版本** —— 算了就是编数字。
	BuildNo *int

	// IsVersioned 为 false 表示这是个非版本化 tag，判定结果必须是「无法判定」。
	IsVersioned bool
}

// Parse 解析一个镜像串。支持三种形态：
//
//	repo:tag
//	repo@sha256:xxx            （pod 的 imageID 常见形态）
//	repo:tag@sha256:xxx
//
// 空串返回零值 Ref，IsVersioned=false —— 调用方据此归入「无法判定」，不要当成错误。
func Parse(image string) Ref {
	s := harborListSuffix.ReplaceAllString(image, "")
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}
	}

	r := Ref{Raw: s}

	// 先摘 digest。@ 只可能出现在 digest 前，不会出现在 registry/项目/名字里
	if at := strings.LastIndex(s, "@"); at >= 0 {
		r.Digest = s[at+1:]
		s = s[:at]
	}

	// 再摘 tag。
	// ⚠️ 必须在**最后一个 / 之后**找冒号 —— registry 可以带端口，
	//    registry.example.com/ops/kite:v0.14.1 里有两个冒号，
	//    按第一个冒号切会把 "8070/proj/kite:v0.14.1" 当成 tag。
	//    这是本地 Harbor（registry.example.com）必然踩到的形态。
	lastSlash := strings.LastIndex(s, "/")
	if colon := strings.LastIndex(s, ":"); colon > lastSlash {
		r.Tag = s[colon+1:]
		s = s[:colon]
	}

	// 剩下的是 repo path，按 / 拆成 registry / project / name
	parts := strings.Split(s, "/")
	r.Name = parts[len(parts)-1]
	switch {
	case len(parts) == 1:
		// 形如 "nginx" —— 官方库镜像，没有 registry 也没有项目
	case isRegistryHost(parts[0]):
		r.Registry = parts[0]
		if len(parts) > 2 {
			r.Project = strings.Join(parts[1:len(parts)-1], "/")
		}
	default:
		// 形如 "nginxinc/nginx-unprivileged" —— 隐式 docker.io，第一段是项目
		r.Project = strings.Join(parts[:len(parts)-1], "/")
	}

	r.IsVersioned = !nonVersionTags[strings.ToLower(r.Tag)]
	// 「stable-otel」这类带后缀的也要挡住：主段是非版本词就算非版本化
	if r.IsVersioned {
		if head, _, ok := strings.Cut(r.Tag, "-"); ok && nonVersionTags[strings.ToLower(head)] {
			r.IsVersioned = false
		}
	}
	if r.IsVersioned {
		r.BuildNo = parseBuildNo(r.Tag)
	}
	return r
}

// isRegistryHost 判断第一段是不是 registry host。
// 判据：含 . 或 :，或者就是 localhost。这是 docker 官方的拆分规则。
func isRegistryHost(s string) bool {
	return strings.ContainsAny(s, ".:") || s == "localhost"
}

// parseBuildNo 从 tag 末段取构建号。
//
// 我们的 tag 有两种形态，末段都是自增构建号：
//
//	20260817032111-80c9c75-70   → 70    时间戳-commit-构建号
//	20260730083740-100          → 100   时间戳-构建号（前端没有 commit 段）
//
// 语义化版本（v0.14.1）取不出构建号，返回 nil。
// 这是**刻意的**：v0.14.1 和 v0.15.0 之间差几个版本无法从字符串得知，
// 硬算出来的数字是假的，不如明说「只能判相同/不同」。
func parseBuildNo(tag string) *int {
	idx := strings.LastIndex(tag, "-")
	if idx < 0 || idx == len(tag)-1 {
		return nil
	}
	n, err := strconv.Atoi(tag[idx+1:])
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

// Key 返回对账 key。可选地套用别名映射 —— 对方改了服务名时，
// 不配别名会同时冒出「仅对方有」+「仅我方有」两条噪音，积累起来这张表就没人看了。
func (r Ref) Key(aliases map[string]string) string {
	if c, ok := aliases[r.Name]; ok {
		return c
	}
	return r.Name
}

// RepoPath 返回不含 registry 的仓库路径，仅用于展示。
func (r Ref) RepoPath() string {
	if r.Project == "" {
		return r.Name
	}
	return r.Project + "/" + r.Name
}

// AllowedBy 判断这个镜像是否参与对账。
//
// nginx / redis 这类公共镜像不该参与 —— 它们不是我们发布的，
// 版本不一致是正常的，混进来只会制造噪音。
// allow 为空表示不过滤（全部参与）。
func (r Ref) AllowedBy(allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, a := range allow {
		if a = strings.TrimSpace(a); a != "" && strings.EqualFold(r.Registry, a) {
			return true
		}
	}
	return false
}

// BuildNoOf 从一个**裸 tag**（不含 repo）取构建号。
// 用于只拿得到 tag 的场景，比如判断一次变更是升级还是回滚。
func BuildNoOf(tag string) *int {
	if nonVersionTags[strings.ToLower(tag)] {
		return nil
	}
	return parseBuildNo(tag)
}
