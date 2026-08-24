-- 项目升为「采集单元」：唯一键从 (org, env) 变成 (org, project, env)。
--
-- ═══════════════════════════════════════════════════════════════
-- 为什么必须这么改
-- ═══════════════════════════════════════════════════════════════
--
-- 加项目层时定的语义是「对比表的一列 = 项目 × 环境」，但存储粒度没跟上：
--
--   org_envs         UNIQUE (org_id, env)              一个平台一个环境只能一行
--   service_versions UNIQUE (org_id, env, service_key) 同名服务只能存一份
--
-- 于是同一个平台下的两个项目各配一个 UAT 时直接撞唯一键（Duplicate entry '3-UAT'）。
--
-- 🔴 第二条约束比第一条更要命：即使放开 org_envs，采回来的数据仍会撞
--    service_versions —— 两个项目只要跑了同名服务（gateway、admin 这类几乎必然重名），
--    同一环境下就**存不下两份**，后写的覆盖先写的，而且不报错。
--    （已确认这个场景真实存在，所以只能走「项目 = 采集单元」这条路。）
--
-- ═══════════════════════════════════════════════════════════════
-- 迁移顺序：先补齐 → 再回填 → 硬校验 → 最后才换索引
-- ═══════════════════════════════════════════════════════════════
--
-- ⚠️ project_id 必须 NOT NULL：MySQL 的唯一索引里 NULL 不参与唯一性判断，
--    留 NULL 等于约束对那些行完全失效 —— 013 迁移在 users 表上栽过一次，
--    表现是"约束建好了，重复数据照样进得来"。

-- ─────────── 1. 每个被引用到的平台都要有一个项目 ───────────
--
-- 包括**已软删的**和**孤儿的**（orgs 里根本没有那条记录）。
-- 不是为了让它们能用，而是为了让回填不留 NULL —— 留一行 NULL，
-- 后面换唯一键就会失败，而那时的报错跟真正的原因隔了十万八千里。
--
-- ⚠️ projects.org_id 没有外键，所以给孤儿 org 也建得出来。这是有意的：
--    说了「不知道成因就删数据，下次还会再长出来」。

INSERT IGNORE INTO projects (org_id, name, sort_order, enabled)
SELECT DISTINCT x.org_id, '默认', 0, 1
  FROM (
    SELECT org_id FROM org_envs
    UNION SELECT org_id FROM service_versions
    UNION SELECT org_id FROM service_pods
    UNION SELECT org_id FROM version_changes
    UNION SELECT id FROM orgs
  ) x
 WHERE NOT EXISTS (
   SELECT 1 FROM projects p WHERE p.org_id = x.org_id AND p.deleted_at IS NULL
 );

-- ─────────── 2. 给三张数据表加列 ───────────

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='service_versions' AND COLUMN_NAME='project_id'),
  'DO 0',
  'ALTER TABLE service_versions ADD COLUMN project_id BIGINT UNSIGNED NULL AFTER org_id');
-- ⚠️ 这三条必须各占一行。
-- 迁移执行器按「行尾分号」切语句（见 runner.go 的 splitSQL），
-- 写在同一行会被当成一条整体送给 MySQL —— Error 1064 语法错误。
-- 🔴 而且这个错**用 mysql CLI 验不出来**：CLI 按分号切，行数无关，
--    所以 CLI 全绿、真正的执行器直接崩。迁移只能用执行器验。
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='service_pods' AND COLUMN_NAME='project_id'),
  'DO 0',
  'ALTER TABLE service_pods ADD COLUMN project_id BIGINT UNSIGNED NULL AFTER org_id');
-- ⚠️ 这三条必须各占一行。
-- 迁移执行器按「行尾分号」切语句（见 runner.go 的 splitSQL），
-- 写在同一行会被当成一条整体送给 MySQL —— Error 1064 语法错误。
-- 🔴 而且这个错**用 mysql CLI 验不出来**：CLI 按分号切，行数无关，
--    所以 CLI 全绿、真正的执行器直接崩。迁移只能用执行器验。
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='version_changes' AND COLUMN_NAME='project_id'),
  'DO 0',
  'ALTER TABLE version_changes ADD COLUMN project_id BIGINT UNSIGNED NULL AFTER org_id');
-- ⚠️ 这三条必须各占一行。
-- 迁移执行器按「行尾分号」切语句（见 runner.go 的 splitSQL），
-- 写在同一行会被当成一条整体送给 MySQL —— Error 1064 语法错误。
-- 🔴 而且这个错**用 mysql CLI 验不出来**：CLI 按分号切，行数无关，
--    所以 CLI 全绿、真正的执行器直接崩。迁移只能用执行器验。
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

-- ─────────── 3. 回填 ───────────
--
-- 优先按 org_envs 已有的归属（014 迁移回填过活跃平台的），
-- 回落到该平台的默认项目（id 最小的那个 —— 014 建的「默认」总是最早的）。

-- 3.1 org_envs 自己
UPDATE org_envs e
   SET e.project_id = (
     SELECT MIN(p.id) FROM projects p
      WHERE p.org_id = e.org_id AND p.deleted_at IS NULL)
 WHERE e.project_id IS NULL;

-- 3.2 三张数据表：先按 (org_id, env) 对上 org_envs
UPDATE service_versions sv
  JOIN org_envs e ON e.org_id = sv.org_id AND e.env = sv.env
   SET sv.project_id = e.project_id
 WHERE sv.project_id IS NULL;

UPDATE service_pods sp
  JOIN org_envs e ON e.org_id = sp.org_id AND e.env = sp.env
   SET sp.project_id = e.project_id
 WHERE sp.project_id IS NULL;

UPDATE version_changes vc
  JOIN org_envs e ON e.org_id = vc.org_id AND e.env = vc.env
   SET vc.project_id = e.project_id
 WHERE vc.project_id IS NULL;

-- 3.3 对不上的（有数据但环境配置已被删）→ 回落到该平台的默认项目
--
-- ⚠️ 这种行确实存在：本地 org_id=4 有 88 条 PROD 数据，而它一个环境映射都没配。
--    把它们丢掉等于悄悄删数据，所以归到默认项目留着。
UPDATE service_versions sv
   SET sv.project_id = (
     SELECT MIN(p.id) FROM projects p
      WHERE p.org_id = sv.org_id AND p.deleted_at IS NULL)
 WHERE sv.project_id IS NULL;

UPDATE service_pods sp
   SET sp.project_id = (
     SELECT MIN(p.id) FROM projects p
      WHERE p.org_id = sp.org_id AND p.deleted_at IS NULL)
 WHERE sp.project_id IS NULL;

UPDATE version_changes vc
   SET vc.project_id = (
     SELECT MIN(p.id) FROM projects p
      WHERE p.org_id = vc.org_id AND p.deleted_at IS NULL)
 WHERE vc.project_id IS NULL;

-- ─────────── 4. 硬校验：还有 NULL 就中止 ───────────
--
-- 🔴 这一步不能省。回填不全就往下换唯一键的话，要么建索引失败
--    （报错是 MySQL 的原文，跟真正的原因隔了十万八千里），
--    要么在非严格模式下 NULL 被悄悄转成 0 —— 那些行从此归属一个不存在的项目，
--    而界面上看不出任何异常。
--
-- 宁可迁移失败、服务起不来，也不要带着一份说不清归属的数据对外服务。

SELECT COUNT(*) INTO @bad FROM (
  SELECT 1 FROM org_envs         WHERE project_id IS NULL
  UNION ALL SELECT 1 FROM service_versions WHERE project_id IS NULL
  UNION ALL SELECT 1 FROM service_pods     WHERE project_id IS NULL
  UNION ALL SELECT 1 FROM version_changes  WHERE project_id IS NULL
) t;

-- ⚠️ 这里**不能用 SIGNAL**：它不支持 prepared statement 协议
--    （`ERROR 1295: This command is not supported in the prepared statement protocol yet`）。
--    而条件执行必须走 PREPARE —— 于是成功路径（DO 0）好好的，
--    中止路径却报一个跟真正原因毫不相干的错。实测栽过。
--
-- 改用「表名即错误信息」：查一张不存在的表，MySQL 会把表名原样写进报错，
-- 而迁移执行器出错时会把语句原文也打出来。两处都在说同一件事。
SET @s := IF(@bad > 0,
  'SELECT * FROM `迁移017中止_回填后仍有行的project_id为空_不能换唯一键`',
  'DO 0');
-- ⚠️ 这三条必须各占一行。
-- 迁移执行器按「行尾分号」切语句（见 runner.go 的 splitSQL），
-- 写在同一行会被当成一条整体送给 MySQL —— Error 1064 语法错误。
-- 🔴 而且这个错**用 mysql CLI 验不出来**：CLI 按分号切，行数无关，
--    所以 CLI 全绿、真正的执行器直接崩。迁移只能用执行器验。
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

-- ─────────── 5. 收紧成 NOT NULL ───────────

ALTER TABLE org_envs         MODIFY project_id BIGINT UNSIGNED NOT NULL COMMENT '所属项目（采集单元的一部分）';
ALTER TABLE service_versions MODIFY project_id BIGINT UNSIGNED NOT NULL COMMENT '所属项目';
ALTER TABLE service_pods     MODIFY project_id BIGINT UNSIGNED NOT NULL COMMENT '所属项目';
ALTER TABLE version_changes  MODIFY project_id BIGINT UNSIGNED NOT NULL COMMENT '所属项目';

-- ─────────── 6. 换唯一键 ───────────
--
-- ⚠️ 先建新的再删旧的会撞（旧键仍在拦），所以先删后建。
--    中间这一小段没有唯一约束 —— 迁移是单连接串行跑的，没有并发写入。

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='org_envs' AND INDEX_NAME='uk_org_env'),
  'ALTER TABLE org_envs DROP INDEX uk_org_env', 'DO 0');
-- ⚠️ 这三条必须各占一行。
-- 迁移执行器按「行尾分号」切语句（见 runner.go 的 splitSQL），
-- 写在同一行会被当成一条整体送给 MySQL —— Error 1064 语法错误。
-- 🔴 而且这个错**用 mysql CLI 验不出来**：CLI 按分号切，行数无关，
--    所以 CLI 全绿、真正的执行器直接崩。迁移只能用执行器验。
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='org_envs' AND INDEX_NAME='uk_org_proj_env'),
  'DO 0',
  'ALTER TABLE org_envs ADD UNIQUE KEY uk_org_proj_env (org_id, project_id, env)');
-- ⚠️ 这三条必须各占一行。
-- 迁移执行器按「行尾分号」切语句（见 runner.go 的 splitSQL），
-- 写在同一行会被当成一条整体送给 MySQL —— Error 1064 语法错误。
-- 🔴 而且这个错**用 mysql CLI 验不出来**：CLI 按分号切，行数无关，
--    所以 CLI 全绿、真正的执行器直接崩。迁移只能用执行器验。
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='service_versions' AND INDEX_NAME='uk_org_env_svc'),
  'ALTER TABLE service_versions DROP INDEX uk_org_env_svc', 'DO 0');
-- ⚠️ 这三条必须各占一行。
-- 迁移执行器按「行尾分号」切语句（见 runner.go 的 splitSQL），
-- 写在同一行会被当成一条整体送给 MySQL —— Error 1064 语法错误。
-- 🔴 而且这个错**用 mysql CLI 验不出来**：CLI 按分号切，行数无关，
--    所以 CLI 全绿、真正的执行器直接崩。迁移只能用执行器验。
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='service_versions' AND INDEX_NAME='uk_org_proj_env_svc'),
  'DO 0',
  'ALTER TABLE service_versions ADD UNIQUE KEY uk_org_proj_env_svc (org_id, project_id, env, service_key)');
-- ⚠️ 这三条必须各占一行。
-- 迁移执行器按「行尾分号」切语句（见 runner.go 的 splitSQL），
-- 写在同一行会被当成一条整体送给 MySQL —— Error 1064 语法错误。
-- 🔴 而且这个错**用 mysql CLI 验不出来**：CLI 按分号切，行数无关，
--    所以 CLI 全绿、真正的执行器直接崩。迁移只能用执行器验。
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

-- 这两张只有普通索引，加上 project_id 让按列取数走得到索引
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='service_pods' AND INDEX_NAME='idx_org_proj_env'),
  'DO 0',
  'ALTER TABLE service_pods ADD KEY idx_org_proj_env (org_id, project_id, env)');
-- ⚠️ 这三条必须各占一行。
-- 迁移执行器按「行尾分号」切语句（见 runner.go 的 splitSQL），
-- 写在同一行会被当成一条整体送给 MySQL —— Error 1064 语法错误。
-- 🔴 而且这个错**用 mysql CLI 验不出来**：CLI 按分号切，行数无关，
--    所以 CLI 全绿、真正的执行器直接崩。迁移只能用执行器验。
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='version_changes' AND INDEX_NAME='idx_org_proj_env_svc'),
  'DO 0',
  'ALTER TABLE version_changes ADD KEY idx_org_proj_env_svc (org_id, project_id, env, service_key, changed_at)');
-- ⚠️ 这三条必须各占一行。
-- 迁移执行器按「行尾分号」切语句（见 runner.go 的 splitSQL），
-- 写在同一行会被当成一条整体送给 MySQL —— Error 1064 语法错误。
-- 🔴 而且这个错**用 mysql CLI 验不出来**：CLI 按分号切，行数无关，
--    所以 CLI 全绿、真正的执行器直接崩。迁移只能用执行器验。
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;
