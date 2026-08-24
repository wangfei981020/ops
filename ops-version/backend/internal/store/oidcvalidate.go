package store

import (
	"fmt"
	"net/url"
	"strings"

	"ops-version-backend/internal/auth"
)

// ValidateOIDC 校验单点登录配置。
//
// 🔴 只在 enabled=true 时严格校验。
//
//	关着的配置允许残缺 —— 人是分几次填完的，每填一段存一次很正常。
//	但**一旦启用**，登录页就会多出一个 SSO 按钮
//	（/api/auth/oidc/status 只看 enabled），点下去跳到一个空地址，
//	登录当场就坏了。而这是**登录页**，影响所有人 ——
//	包括还没登录进来、没法自己改回配置的人。
//
// ⚠️ 校验放在 store 层而不是 handler：将来多一个调用点（导入配置、
//
//	命令行工具）就会绕过 handler 里的校验，而绕过时不报错。
func ValidateOIDC(c OIDCConfig, hasSecret bool) error {
	if !c.Enabled {
		return nil
	}
	var missing []string
	required := []struct {
		name string
		val  string
	}{
		{"发行者 issuer", c.Issuer},
		{"client_id", c.ClientID},
		{"授权地址 authorize_url", c.AuthorizeURL},
		{"令牌地址 token_url", c.TokenURL},
	}
	for _, f := range required {
		if strings.TrimSpace(f.val) == "" {
			missing = append(missing, f.name)
		}
	}
	if !hasSecret {
		missing = append(missing, "client_secret")
	}
	if len(missing) > 0 {
		return fmt.Errorf("启用单点登录前这几项必须填：%s。"+
			"⚠️ 现在保存的话，登录页会多出一个 SSO 按钮，点下去跳到空地址 —— "+
			"而那是**所有人**的登录入口",
			strings.Join(missing, "、"))
	}

	// 地址要能真的跳过去。
	// ⚠️ 只挡明显不可用的（缺 scheme / 缺 host）——
	//    不做更严的校验：IdP 的地址形态五花八门，挡太狠会把能用的配置也拦掉。
	for _, f := range []struct {
		name string
		val  string
	}{
		{"授权地址", c.AuthorizeURL},
		{"令牌地址", c.TokenURL},
		{"用户信息地址", c.UserinfoURL},
	} {
		v := strings.TrimSpace(f.val)
		if v == "" {
			continue // userinfo 可选：有些 IdP 把 claim 全放在 id_token 里
		}
		u, err := url.Parse(v)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("%s 不是一个可用的地址：%q —— 要带 http(s):// 和域名", f.name, v)
		}
	}

	// 默认角色写错会让「一条组都没命中」的人拿到零权限，
	// 而界面上那个角色码看着好好的（IsKnownRole 的同一条理由）
	if r := strings.TrimSpace(c.DefaultRole); r != "" && !auth.RoleValid(r) {
		return fmt.Errorf("默认角色 %q 不存在 —— 组映射没命中的人会拿到零权限，"+
			"而配置页上看不出任何异常", r)
	}
	return nil
}
