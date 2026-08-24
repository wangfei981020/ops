-- 自定义角色 + 角色锁定。
--
-- 两件事一起做，因为它们解决的是同一个矛盾：
-- **IdP 的组和本系统的角色对不齐**。
--   自定义角色 —— 对方的组划分和内置四个角色对不上时，自己造一个
--   角色锁定   —— 某个人就是要一个跟组不一样的角色，手工定死

-- ─────────── 角色表 ───────────
--
-- 🔴 内置四个角色也写进表里，但 builtin=1 且**不可改不可删**。
--    不写进表：前端要拿角色清单就得把内置的和自定义的两处拼起来，
--             而"拼"这件事迟早有一处漏掉（新加的自定义角色在某个下拉里不出现）。
--    可以改：那 admin 的权限就成了浮动的，出事后没人说得清当时它包含什么。
CREATE TABLE IF NOT EXISTS roles (
  id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  code       VARCHAR(32)  NOT NULL COMMENT '角色码，写进 users.role_code',
  name       VARCHAR(64)  NOT NULL COMMENT '给人看的名字',
  -- 逗号分隔的权限码。用逗号串而不是关联表：权限总共九项，
  -- 一张关联表换来的是每次读角色都要多一次 JOIN，不划算。
  -- ⚠️ 读出来必须 split 成数组再给前端。直接把逗号串塞给 string[] 会让整页崩
  --    （某个同类产品 栽过，见 project_cmdb_domain_ui_wiring）。
  perms      VARCHAR(512) NOT NULL DEFAULT '',
  builtin    TINYINT(1)   NOT NULL DEFAULT 0 COMMENT '1=内置，不可改不可删',
  note       VARCHAR(255) NOT NULL DEFAULT '',
  created_by VARCHAR(64)  NOT NULL DEFAULT '',
  created_at DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at DATETIME     NULL,
  -- 生成列 + 唯一索引：直接 (code, deleted_at) 的话 NULL 不参与唯一判断，
  -- 约束对活跃行形同虚设（013 迁移踩过）
  alive_code VARCHAR(32)  GENERATED ALWAYS AS (IF(deleted_at IS NULL, code, NULL)) STORED,
  PRIMARY KEY (id),
  UNIQUE KEY uk_role_alive_code (alive_code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='角色（内置 + 自定义）';

-- 内置四个角色。权限清单必须与 internal/auth/rbac.go 里的 rolePerms 完全一致 ——
-- ⚠️ 两处分叉的表现是"界面上显示有这个权限、点了却 403"，
--    所以代码启动时会校验，对不上直接拒绝启动（见 auth.Registry）。
INSERT IGNORE INTO roles (code, name, perms, builtin, note) VALUES
  ('super_admin', '超级管理员',
   'view,export,refresh,plan.write,org.write,sync.trigger,alert.write,audit.view,user.admin',
   1, '全部权限，含用户与角色管理'),
  ('admin', '管理员',
   'view,export,refresh,plan.write,org.write,sync.trigger,alert.write,audit.view',
   1, '管平台、方案、告警、审计，不能管用户'),
  ('editor', '编辑',
   'view,export,refresh,plan.write',
   1, '能刷新和管方案，不能改平台配置'),
  ('viewer', '只读',
   'view,export',
   1, '只能看和导出');

-- ─────────── 角色锁定 ───────────
--
-- 🔴 为什么要锁：SSO 用户每次登录都会按组重新映射角色（JIT 对老用户也跑）。
--    管理员在界面上手工改的角色，下一次 SSO 登录就被覆盖回去 ——
--    表现是"我明明改了，他登录一次又变回来了"，而且没有任何提示。
--
-- 🔴 为什么锁要有期限：**永久锁会悄悄和 IdP 脱节**。
--    人调岗了、从组里移出去了，锁着的角色还在，而没有任何东西提醒你去看。
--    加一个到期日，等于强制每隔一段时间重新确认一次这个例外还成不成立。
--    默认 90 天由后端给，不写死在库里 —— 那是策略不是数据。
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND COLUMN_NAME='role_locked_until'),
  'DO 0',
  'ALTER TABLE users ADD COLUMN role_locked_until DATETIME NULL COMMENT ''锁到什么时候；NULL=不锁，SSO 登录会按组覆盖角色''');
PREPARE st FROM @s;
EXECUTE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND COLUMN_NAME='role_lock_reason'),
  'DO 0',
  'ALTER TABLE users ADD COLUMN role_lock_reason VARCHAR(255) NOT NULL DEFAULT '''' COMMENT ''为什么要给这个人开例外 —— 到期复核时要看''');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;
