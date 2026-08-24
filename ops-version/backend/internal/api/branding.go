package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"ops-version-backend/internal/store"
)

// 品牌自定义（白标）。
//
// 🔴 上传时限制，不在渲染时裁剪 ——
// 渲染时裁剪会让"我传的图"和"显示出来的图"不一样，而那种问题没人查得出来。
// 这里只做**能自动判定**的检查（类型、大小），比例之类的交给人眼。
const (
	// maxLogoBytes 原始图片上限。
	// logo 存库（见 012 迁移的说明），太大了会让每次读配置都拖一大坨 BLOB。
	// 256KB 足够放一张清晰的 SVG 或 PNG。
	maxLogoBytes = 256 * 1024
	// favicon 更小：它只在 16–64px 显示，传大图纯属浪费
	maxFaviconBytes = 64 * 1024
)

// allowedImageTypes 允许的 data URI 前缀。
//
// ⚠️ 刻意**不允许** SVG 之外的矢量格式，也不允许任意 data URI：
// data URI 可以塞 `text/html`，而它会在 <img> 之外的地方被当成脚本执行。
// 白名单比黑名单安全 —— 这是个管理员能改、所有人能看到的字段。
var allowedImageTypes = []string{
	"data:image/png;base64,",
	"data:image/jpeg;base64,",
	"data:image/webp;base64,",
	"data:image/svg+xml;base64,",
	"data:image/x-icon;base64,",
	"data:image/vnd.microsoft.icon;base64,",
}

// getBranding 读品牌配置。
//
// ⚠️ **不要求登录**：登录页和 favicon 都要用它，而那时候人还没登录。
// 里面没有任何敏感信息（就是几张图和一个名字），公开可读是可以接受的。
func (s *Server) getBranding(w http.ResponseWriter, r *http.Request) {
	b, err := s.St.GetBranding(r.Context())
	if err != nil {
		// 🔴 读不到品牌配置不能让页面打不开 —— 退回默认（全空），
		//    产品会用自己内置的名字和图标。
		//    这里报错的话，一次数据库抖动会让所有人连登录页都看不到。
		ok(w, store.Branding{})
		return
	}
	ok(w, b)
}

func (s *Server) saveBranding(w http.ResponseWriter, r *http.Request) {
	var in store.Branding
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	if msg := validateImage(in.LogoData, maxLogoBytes, "Logo"); msg != "" {
		fail(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	if msg := validateImage(in.FaviconData, maxFaviconBytes, "浏览器图标"); msg != "" {
		fail(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	err := s.St.SaveBranding(r.Context(), in, userOf(r).Username)
	s.St.Audit(r.Context(), userOf(r).Username, "branding.save", "branding", nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

// validateImage 校验一张 data URI 图片。空串合法（表示清空/不设置）。
func validateImage(v string, maxBytes int, label string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	var okType bool
	for _, p := range allowedImageTypes {
		if strings.HasPrefix(v, p) {
			okType = true
			break
		}
	}
	if !okType {
		return label + "：只支持 PNG / JPEG / WebP / SVG / ICO，且必须是 base64 的 data URI"
	}
	i := strings.IndexByte(v, ',')
	raw, err := base64.StdEncoding.DecodeString(v[i+1:])
	if err != nil {
		// 🔴 解不出来就拒绝，不能存进去。
		//    存了的话，界面上到处是破图，而"图坏了"比"没有图"更像系统故障。
		return label + "：图片数据损坏（base64 解码失败）"
	}
	if len(raw) > maxBytes {
		return label + "：图片太大（" + humanKB(len(raw)) + "），上限 " + humanKB(maxBytes)
	}
	return ""
}

func humanKB(n int) string {
	kb := (n + 1023) / 1024
	return itoa(kb) + " KB"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
