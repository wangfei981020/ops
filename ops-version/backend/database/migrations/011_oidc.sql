-- OIDC 单点登录 + 组/角色映射。
--
-- 背景：客户用 MXID 做统一身份，希望「从 IdP 下发的组或角色」自动决定这里的权限，
-- 而不是每来一个人再手工建号授权。

-- ─────────── OIDC 配置 ───────────
--
-- 刻意只允许**一条**：多 IdP 会带来"同一个人从两个入口进来算不算同一个账号"的问题，
-- 而那个问题没有普适答案。真需要多个时再谈，别现在留半成品。
CREATE TABLE IF NOT EXISTS oidc_config (
  id              TINYINT UNSIGNED NOT NULL DEFAULT 1,
  enabled         TINYINT(1)   NOT NULL DEFAULT 0,
  display_name    VARCHAR(64)  NOT NULL DEFAULT 'SSO' COMMENT '登录页按钮上的字',
  issuer          VARCHAR(255) NOT NULL DEFAULT '',
  client_id       VARCHAR(255) NOT NULL DEFAULT '',
  client_secret_enc TEXT       NULL COMMENT '加密存储，永不回显',
  -- 🔴 三个端点允许手填，不能只依赖 issuer 自动发现。
  -- 实测同事的 MXID：generic OIDC 客户端按 /.well-known/openid-configuration 探不到，
  -- 必须手填非标端点（Grafana 接 MXID 时就栽在这）。
  authorize_url   VARCHAR(255) NOT NULL DEFAULT '',
  token_url       VARCHAR(255) NOT NULL DEFAULT '',
  userinfo_url    VARCHAR(255) NOT NULL DEFAULT '',
  scopes          VARCHAR(255) NOT NULL DEFAULT 'openid profile email',
  -- 🔴 组来自哪个 claim。MXID 有两个长得很像但**含义不同**的字段：
  --   groups     = 用户组 code（组织架构）
  --   app_roles  = 应用角色（这个系统里该是什么角色）← 通常要的是这个
  -- 填错了会导致"所有人都匹配不上任何映射"，然后全都掉进默认角色。
  groups_claim    VARCHAR(64)  NOT NULL DEFAULT 'app_roles',
  username_claim  VARCHAR(64)  NOT NULL DEFAULT 'preferred_username',
  -- 🔴 一个组都没匹配上时给什么角色。默认 viewer（只读）——
  -- 留空/给 admin 都是危险默认：前者让人登进来什么都看不到以为系统坏了，
  -- 后者把没配映射的人全变成管理员。
  default_role    VARCHAR(32)  NOT NULL DEFAULT 'viewer',
  -- 关掉后，未在映射表里命中的人直接拒绝登录（而不是给 default_role）
  allow_unmapped  TINYINT(1)   NOT NULL DEFAULT 1,
  insecure_tls    TINYINT(1)   NOT NULL DEFAULT 0,
  updated_at      DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='OIDC 单点登录配置（单条）';

INSERT IGNORE INTO oidc_config (id) VALUES (1);

-- ─────────── 组 → 角色映射 ───────────
--
-- ⚠️ 一个人可能同时命中多条。取**权限并集**而不是"第一条命中"：
-- 后者的结果取决于表的顺序，加一条映射就可能悄悄改变别人的权限。
CREATE TABLE IF NOT EXISTS oidc_role_mappings (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  group_value VARCHAR(191) NOT NULL COMMENT 'IdP 下发的组/角色值，支持 * 通配',
  role_code   VARCHAR(32)  NOT NULL COMMENT 'super_admin | admin | editor | viewer',
  note        VARCHAR(255) NOT NULL DEFAULT '',
  created_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deleted_at  DATETIME     NULL,
  PRIMARY KEY (id),
  -- ⚠️ 唯一索引必须带 deleted_at：不带的话，删掉一条再建同名的会撞唯一键，
  --    而软删的行在界面上根本看不见 —— 表现是"这个组名不能用"，没人知道为什么
  UNIQUE KEY uk_group (group_value, deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='IdP 组/角色 → 本系统角色';

-- ─────────── 用户表补字段 ───────────
--
-- 记住这个人上次登录时 IdP 下发了什么组 —— 排障时最常问的就是
-- "他为什么是这个角色"，没有这个字段只能去翻 IdP。
-- ⚠️ 条件 DDL 的三句（PREPARE / EXECUTE / DEALLOCATE）必须**各占一行**：
-- runner 按 `;` 切语句，写成一行会被当作一条整体送给 MySQL，报 1064 语法错。
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND COLUMN_NAME='oidc_groups'),
  'DO 0',
  'ALTER TABLE users ADD COLUMN oidc_groups VARCHAR(500) NOT NULL DEFAULT '''' COMMENT ''上次登录时 IdP 下发的组，逗号分隔''');
PREPARE st FROM @s;
EXECUTE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND COLUMN_NAME='oidc_subject'),
  'DO 0',
  'ALTER TABLE users ADD COLUMN oidc_subject VARCHAR(191) NOT NULL DEFAULT '''' COMMENT ''IdP 的 sub，换 IdP 时靠它认人''');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;
