package store

import (
	"strings"
	"testing"
)

func fullOIDC() OIDCConfig {
	return OIDCConfig{
		Enabled: true, Issuer: "https://idp.example.com",
		ClientID: "cid", AuthorizeURL: "https://idp.example.com/authorize",
		TokenURL: "https://idp.example.com/token",
		UserinfoURL: "https://idp.example.com/userinfo",
	}
}

// 🔴 不能存出「已启用但地址全空」的配置。
//
// 那会让登录页多出一个 SSO 按钮，点下去跳到空地址 ——
// 而登录页是所有人的入口，包括还没进来、没法自己改回配置的人。
func TestOIDCEnabledRequiresFields(t *testing.T) {
	err := ValidateOIDC(OIDCConfig{Enabled: true}, false)
	if err == nil {
		t.Fatal("全空却启用，必须拒绝")
	}
	for _, want := range []string{"issuer", "client_id", "authorize_url", "token_url", "client_secret"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误里没点名缺了 %s —— 人不知道要补什么\n实得：%v", want, err)
		}
	}
}

// ⚠️ 关着的配置允许残缺：人是分几次填完的，每填一段存一次很正常
func TestOIDCDisabledAllowsPartial(t *testing.T) {
	if err := ValidateOIDC(OIDCConfig{Enabled: false, Issuer: "只填了一半"}, false); err != nil {
		t.Errorf("没启用就不该拦：%v", err)
	}
}

// 🔴 改个显示名不该被要求重填密钥。
//
// 密钥永不回显，前端提交时那一栏是空的 ——
// 把「这次没传」当成「没有」的话，每次改任何字段都要重输密钥。
func TestOIDCExistingSecretCounts(t *testing.T) {
	c := fullOIDC()
	if err := ValidateOIDC(c, true); err != nil {
		t.Errorf("库里已有密钥就该放行：%v", err)
	}
	if err := ValidateOIDC(c, false); err == nil {
		t.Error("确实没有密钥时要拦")
	}
}

// 地址要能真跳过去
func TestOIDCRejectsBadURL(t *testing.T) {
	c := fullOIDC()
	c.AuthorizeURL = "idp.example.com/authorize" // 少了 scheme
	err := ValidateOIDC(c, true)
	if err == nil || !strings.Contains(err.Error(), "授权地址") {
		t.Errorf("缺 scheme 的地址应被拦下并点名是哪一项，实得 %v", err)
	}
}

// ⚠️ userinfo 可选：有些 IdP 把 claim 全放在 id_token 里
func TestOIDCUserinfoOptional(t *testing.T) {
	c := fullOIDC()
	c.UserinfoURL = ""
	if err := ValidateOIDC(c, true); err != nil {
		t.Errorf("userinfo 是可选的：%v", err)
	}
}

// 默认角色写错 = 没命中组的人拿到零权限，而配置页看不出异常
func TestOIDCRejectsUnknownDefaultRole(t *testing.T) {
	c := fullOIDC()
	c.DefaultRole = "vieweer"
	if err := ValidateOIDC(c, true); err == nil {
		t.Error("默认角色不存在必须拦 —— 否则没命中组的人静默变成零权限")
	}
	c.DefaultRole = "viewer"
	if err := ValidateOIDC(c, true); err != nil {
		t.Errorf("合法角色被拦了：%v", err)
	}
}
