-- 六张表的「软删唯一键」全都形同虚设。
--
-- ═══════════════════════════════════════════════════════════
-- 🔴 `UNIQUE (name, deleted_at)` 对活跃行**没有任何约束力**
-- ═══════════════════════════════════════════════════════════
--
-- MySQL 的唯一索引里 **NULL 不参与唯一性判断** —— 两行 (name='X', deleted_at=NULL)
-- 在它看来不算重复。而活跃行的 deleted_at 恰恰全是 NULL。
--
-- 实测（本地库）：连着插两条同名的活跃平台，两条都进去了。
--
--   id  name        deleted_at
--   13  重名测试    NULL
--   14  重名测试    NULL
--
-- 后果按表不同：
--   orgs             → 比对表出现两个同名列，谁也分不清哪个是哪个
--   users            → 同名账号两份，改权限改到哪一份看运气（013 迁移修过 users，
--                      但只加了 uk_alive_username，**旧的坏索引没删**，见下）
--   comparison_plans → 同名方案两份
--   harbors / notify_channels / oidc_role_mappings → 同上
--
-- ⚠️ 这条坑 013 迁移里已经写明白过，而其余五张表当时没一起改 ——
--    「修了撞到的那一处，没去找同一形状的其他处」。
--
-- ═══════════════════════════════════════════════════════════
-- 正解：生成列 + 唯一索引
-- ═══════════════════════════════════════════════════════════
--
--   alive_x = IF(deleted_at IS NULL, x, NULL)
--   UNIQUE (alive_x)
--
-- 活跃行的 alive_x = 真实值 → 参与唯一判断；
-- 已删行的 alive_x = NULL   → 不参与，同名可以反复删了再建。
--
-- ⚠️ 加索引前必须先处理**已经存在的重复活跃行**，否则建索引直接失败。
--    处理方式是把旧的那几条软删掉（保留 id 最大的那条 = 最后写入的），
--    而不是硬删 —— 它们可能还被别处引用着。

-- ─────────── 1. 先软删掉重复的活跃行（保留 id 最大的）───────────
--
-- ⚠️ deleted_at 用 DATE_SUB(NOW(), INTERVAL id SECOND) 而不是统一的 NOW()：
--    统一时间的话，被软删的那几条之间又会撞上**旧的** (name, deleted_at) 索引
--    —— 1062。013 迁移踩过这个，原样照搬解法。

UPDATE orgs o
  JOIN (SELECT name, MAX(id) keep FROM orgs WHERE deleted_at IS NULL GROUP BY name HAVING COUNT(*) > 1) d
    ON d.name = o.name
   SET o.deleted_at = DATE_SUB(NOW(), INTERVAL o.id SECOND), o.enabled = 0
 WHERE o.deleted_at IS NULL AND o.id <> d.keep;

UPDATE comparison_plans p
  JOIN (SELECT name, MAX(id) keep FROM comparison_plans WHERE deleted_at IS NULL GROUP BY name HAVING COUNT(*) > 1) d
    ON d.name = p.name
   SET p.deleted_at = DATE_SUB(NOW(), INTERVAL p.id SECOND)
 WHERE p.deleted_at IS NULL AND p.id <> d.keep;

UPDATE harbors h
  JOIN (SELECT name, MAX(id) keep FROM harbors WHERE deleted_at IS NULL GROUP BY name HAVING COUNT(*) > 1) d
    ON d.name = h.name
   SET h.deleted_at = DATE_SUB(NOW(), INTERVAL h.id SECOND)
 WHERE h.deleted_at IS NULL AND h.id <> d.keep;

UPDATE notify_channels c
  JOIN (SELECT name, MAX(id) keep FROM notify_channels WHERE deleted_at IS NULL GROUP BY name HAVING COUNT(*) > 1) d
    ON d.name = c.name
   SET c.deleted_at = DATE_SUB(NOW(), INTERVAL c.id SECOND)
 WHERE c.deleted_at IS NULL AND c.id <> d.keep;

UPDATE oidc_role_mappings m
  JOIN (SELECT group_value, MAX(id) keep FROM oidc_role_mappings WHERE deleted_at IS NULL GROUP BY group_value HAVING COUNT(*) > 1) d
    ON d.group_value = m.group_value
   SET m.deleted_at = DATE_SUB(NOW(), INTERVAL m.id SECOND)
 WHERE m.deleted_at IS NULL AND m.id <> d.keep;

-- ─────────── 2. 加生成列 ───────────

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='orgs' AND COLUMN_NAME='alive_name'),
  'DO 0',
  'ALTER TABLE orgs ADD COLUMN alive_name VARCHAR(128) GENERATED ALWAYS AS (IF(deleted_at IS NULL, name, NULL)) STORED');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='comparison_plans' AND COLUMN_NAME='alive_name'),
  'DO 0',
  'ALTER TABLE comparison_plans ADD COLUMN alive_name VARCHAR(128) GENERATED ALWAYS AS (IF(deleted_at IS NULL, name, NULL)) STORED');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='harbors' AND COLUMN_NAME='alive_name'),
  'DO 0',
  'ALTER TABLE harbors ADD COLUMN alive_name VARCHAR(128) GENERATED ALWAYS AS (IF(deleted_at IS NULL, name, NULL)) STORED');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='notify_channels' AND COLUMN_NAME='alive_name'),
  'DO 0',
  'ALTER TABLE notify_channels ADD COLUMN alive_name VARCHAR(128) GENERATED ALWAYS AS (IF(deleted_at IS NULL, name, NULL)) STORED');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='oidc_role_mappings' AND COLUMN_NAME='alive_group'),
  'DO 0',
  'ALTER TABLE oidc_role_mappings ADD COLUMN alive_group VARCHAR(191) GENERATED ALWAYS AS (IF(deleted_at IS NULL, group_value, NULL)) STORED');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

-- ─────────── 3. 建新唯一索引 ───────────

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='orgs' AND INDEX_NAME='uk_org_alive_name'),
  'DO 0', 'ALTER TABLE orgs ADD UNIQUE KEY uk_org_alive_name (alive_name)');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='comparison_plans' AND INDEX_NAME='uk_plan_alive_name'),
  'DO 0', 'ALTER TABLE comparison_plans ADD UNIQUE KEY uk_plan_alive_name (alive_name)');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='harbors' AND INDEX_NAME='uk_harbor_alive_name'),
  'DO 0', 'ALTER TABLE harbors ADD UNIQUE KEY uk_harbor_alive_name (alive_name)');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='notify_channels' AND INDEX_NAME='uk_chan_alive_name'),
  'DO 0', 'ALTER TABLE notify_channels ADD UNIQUE KEY uk_chan_alive_name (alive_name)');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='oidc_role_mappings' AND INDEX_NAME='uk_mapping_alive_group'),
  'DO 0', 'ALTER TABLE oidc_role_mappings ADD UNIQUE KEY uk_mapping_alive_group (alive_group)');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

-- ─────────── 4. 删掉那些形同虚设的旧索引 ───────────
--
-- ⚠️ 必须删。留着的话：
--   ① 它们仍然拦「同名 + 同一 deleted_at 时刻」的组合 —— 于是批量软删时
--      会莫名其妙撞 1062（013 迁移就是被这个绊住的）
--   ② 后来的人看到 `UNIQUE (name, deleted_at)` 会以为唯一性已经有人管了

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='orgs' AND INDEX_NAME='uk_org_name'),
  'ALTER TABLE orgs DROP INDEX uk_org_name', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='comparison_plans' AND INDEX_NAME='uk_plan_name'),
  'ALTER TABLE comparison_plans DROP INDEX uk_plan_name', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='harbors' AND INDEX_NAME='uk_name'),
  'ALTER TABLE harbors DROP INDEX uk_name', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='notify_channels' AND INDEX_NAME='uk_chan_name'),
  'ALTER TABLE notify_channels DROP INDEX uk_chan_name', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='oidc_role_mappings' AND INDEX_NAME='uk_group'),
  'ALTER TABLE oidc_role_mappings DROP INDEX uk_group', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

-- users 表 013 迁移已经加过 uk_alive_username，这里只补删旧索引
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND INDEX_NAME='uk_username'),
  'ALTER TABLE users DROP INDEX uk_username', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;
