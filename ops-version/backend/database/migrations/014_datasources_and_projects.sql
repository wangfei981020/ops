-- 数据源独立 + 项目层。
--
-- 背景：现在「平台(orgs)」一个概念同时承担三件事 ——
--   ① 连接源（地址+凭据）  ② 归属主体（哪家公司）  ③ 对比单元（表格的一列）
-- 而实际情况里这三者是分开的：
--   多家公司共用一个 Rancher（连接源复用）
--   一家公司下有多个项目，各自要一列（对比单元 ≠ 归属主体）
--   多个项目可能挤在同一个命名空间（区分不能只靠 ns）

-- ─────────── 数据源 ───────────
--
-- 🔴 独立出来的理由：同一个 Rancher/ArgoCD/Kite 被 N 家公司共用时，
-- 现在要把地址和凭据重复配 N 遍 —— 改一次密码要改 N 处，漏一处就是一个平台悄悄采集失败。
CREATE TABLE IF NOT EXISTS datasources (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name          VARCHAR(64)  NOT NULL COMMENT '给人看的名字，如 asia-dev-rancher',
  provider_type VARCHAR(32)  NOT NULL COMMENT 'kite | rancher | argocd',
  endpoint      VARCHAR(255) NOT NULL DEFAULT '',
  auth_type     VARCHAR(32)  NOT NULL DEFAULT '' COMMENT 'password | api_key | token',
  credential_enc TEXT        NULL COMMENT '加密存储，永不回显',
  insecure_tls  TINYINT(1)   NOT NULL DEFAULT 0,
  enabled       TINYINT(1)   NOT NULL DEFAULT 1,
  created_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at    DATETIME     NULL,
  -- ⚠️ 生成列 + 唯一索引：直接用 (name, deleted_at) 的话，NULL 不参与唯一判断，
  --    约束对活跃行形同虚设（013 迁移踩过，users 表因此每次登录插一行）
  alive_name    VARCHAR(64)  GENERATED ALWAYS AS (IF(deleted_at IS NULL, name, NULL)) STORED,
  PRIMARY KEY (id),
  UNIQUE KEY uk_ds_alive_name (alive_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='数据源（连接信息，可被多个平台共用）';

-- ─────────── 项目 ───────────
--
-- 一家公司下的一个项目。对比表的一列 = 项目 × 环境。
CREATE TABLE IF NOT EXISTS projects (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  org_id      BIGINT UNSIGNED NOT NULL,
  name        VARCHAR(64)  NOT NULL COMMENT '项目名，如 G66',
  -- 🔴 同一个 ns 里区分多项目的三档（按优先级，够用即止）：
  --   ① ns 隔离      → 环境的 ns 规则就够，这里留空
  --   ② 名字有规律   → service_include 写通配，如 biz-*
  --   ③ 无规律       → service_pins 存确切服务名（从已采集列表勾选，**不让人手打**）
  service_include TEXT     NULL COMMENT '服务名通配，一行一个',
  service_pins    TEXT     NULL COMMENT '手工指定的确切服务名，一行一个',
  sort_order  INT          NOT NULL DEFAULT 0,
  enabled     TINYINT(1)   NOT NULL DEFAULT 1,
  created_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at  DATETIME     NULL,
  alive_key   VARCHAR(96)  GENERATED ALWAYS AS
                (IF(deleted_at IS NULL, CONCAT(org_id, ':', name), NULL)) STORED,
  PRIMARY KEY (id),
  UNIQUE KEY uk_proj_alive (alive_key),
  KEY idx_proj_org (org_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='项目（一家公司下的一个项目）';

-- ─────────── 平台引用数据源 ───────────
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='orgs' AND COLUMN_NAME='datasource_id'),
  'DO 0',
  'ALTER TABLE orgs ADD COLUMN datasource_id BIGINT UNSIGNED NULL COMMENT ''引用的数据源；NULL=用平台自己的连接信息（迁移期兼容）''');
PREPARE st FROM @s;
EXECUTE st;

-- ─────────── 环境挂到项目 ───────────
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='org_envs' AND COLUMN_NAME='project_id'),
  'DO 0',
  'ALTER TABLE org_envs ADD COLUMN project_id BIGINT UNSIGNED NULL COMMENT ''所属项目；NULL=该平台的默认项目''');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

-- ─────────── 自动迁移现有配置 ───────────
--
-- 🔴 老配置必须零改动地继续工作，否则升级即中断服务。
-- 每个平台生成：一个同名数据源（带走连接信息）+ 一个默认项目（承接现有环境）。
-- ⚠️ 数据源名可能撞（两个平台同名不可能，但平台名可能与已有数据源名撞），
--    所以用 INSERT IGNORE + 事后按名字回填 id。

INSERT IGNORE INTO datasources (name, provider_type, endpoint, auth_type, credential_enc, enabled)
SELECT o.name, o.provider_type, o.endpoint, o.auth_type, o.credential_enc, 1
  FROM orgs o
 WHERE o.deleted_at IS NULL
   AND o.provider_type IN ('kite', 'rancher', 'argocd')
   AND NOT EXISTS (SELECT 1 FROM datasources d WHERE d.alive_name = o.name);

UPDATE orgs o
  JOIN datasources d ON d.alive_name = o.name
   SET o.datasource_id = d.id
 WHERE o.deleted_at IS NULL AND o.datasource_id IS NULL;

-- 默认项目：名字用「默认」，与平台一对一
INSERT IGNORE INTO projects (org_id, name, sort_order, enabled)
SELECT o.id, '默认', 0, 1
  FROM orgs o
 WHERE o.deleted_at IS NULL
   AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.org_id = o.id AND p.deleted_at IS NULL);

UPDATE org_envs e
  JOIN projects p ON p.org_id = e.org_id AND p.deleted_at IS NULL AND p.name = '默认'
   SET e.project_id = p.id
 WHERE e.project_id IS NULL;
