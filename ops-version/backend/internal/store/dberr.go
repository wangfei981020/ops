package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// friendlyDBErr 把 MySQL 的原始错误翻成人话。
//
// 🔴 直接把 `Duplicate entry '3-UAT' for key 'org_envs.uk_org_env'` 抛给用户
// 是没有意义的：他不知道 3 是 org_id、不知道 uk_org_env 是什么索引，
// 更不知道下一步该做什么。用户实测就撞到过这一条。
//
// ⚠️ 只翻译**认得出来的**。认不出的原样返回 —— 编一句笼统的
// 「保存失败，请重试」会把真正的原因盖掉，比原始错误更糟。
func friendlyDBErr(err error) error {
	var me *mysql.MySQLError
	if !errors.As(err, &me) {
		return err
	}
	switch me.Number {
	case 1062: // Duplicate entry
		switch {
		case strings.Contains(me.Message, "uk_org_proj_env_svc"):
			return fmt.Errorf("这个项目在这个环境上已经有同名服务的记录了")
		case strings.Contains(me.Message, "uk_org_proj_env"):
			return fmt.Errorf("这个项目在这个环境上已经配过了 —— " +
				"一个项目的一个环境只能配一行。要给**另一个**项目配同名环境的话，" +
				"先在上面的「项目」区把那个项目建出来，再在环境的「所属项目」里选它")
		case strings.Contains(me.Message, "uk_org_name"):
			return fmt.Errorf("平台名已存在")
		case strings.Contains(me.Message, "uk_proj_alive"):
			return fmt.Errorf("这个平台下已经有同名项目了")
		case strings.Contains(me.Message, "uk_ds_alive_name"):
			return fmt.Errorf("数据源名已存在")
		case strings.Contains(me.Message, "uk_role_alive_code"):
			return fmt.Errorf("角色码已存在")
		case strings.Contains(me.Message, "uk_alive_username"):
			return fmt.Errorf("用户名已存在")
		}
		return fmt.Errorf("有重复的记录：%s", me.Message)
	case 1452: // 外键约束
		return fmt.Errorf("引用了一个不存在的记录（可能刚被别人删掉了），刷新页面再试")
	}
	return err
}
