-- 修复 users 表的唯一约束对**活跃用户**失效的问题。
--
-- 🔴 原索引 `UNIQUE KEY uk_username (username, deleted_at)`：
-- MySQL 的唯一索引里 **NULL 不参与唯一性判断**，而活跃用户的 deleted_at 恒为 NULL，
-- 于是这个约束对活跃行**形同虚设** —— 可以插出任意多个同名活跃用户。
--
-- 实际后果：
-- `INSERT ... ON DUPLICATE KEY UPDATE` 永远不触发，每次 SSO 登录新插一行；
-- 而按 username 查只取第一行（最早那条），于是会话绑到旧行，
-- 表现为「改了组→角色映射、重新登录、角色纹丝不动」，
-- 而后端日志里算出来的角色明明是对的。
--
-- 索引本身的意图（软删后同名可重建）是对的，只是写法拿不到那个语义。
-- 正解是**生成列**：活跃行取 username、软删行取 NULL，再对它加唯一索引 ——
-- NULL 不参与唯一判断这条特性，在这里恰好变成我们要的东西。

-- ── ① 先清理已经产生的重复活跃行 ──
--
-- ⚠️ 必须先清理，否则加唯一索引会直接失败（生产上已经有重复了）。
-- 保留 **id 最小**那条（最早创建的，会话和审计都指向它），其余软删。
-- 🔴 不能物理删除：sessions / audit_logs 里可能引用着它们的 id，
--    删了会让历史记录指向不存在的用户。
-- ⚠️ 软删时间必须**逐行不同**：旧索引是 (username, deleted_at)，
--    如果两条重复行被软删成同一个 NOW()，它们之间又撞唯一键，迁移直接 1062 失败。
--    （实测撞到过 —— 清理重复数据的语句自己被那个有缺陷的索引挡住了。）
--    用 id 做偏移，保证每行拿到不同的时间戳。
UPDATE users u
  JOIN (
    SELECT username, MIN(id) AS keep_id
      FROM users WHERE deleted_at IS NULL
     GROUP BY username HAVING COUNT(*) > 1
  ) d ON d.username = u.username
   SET u.deleted_at = DATE_SUB(NOW(), INTERVAL u.id SECOND)
 WHERE u.deleted_at IS NULL AND u.id <> d.keep_id;

-- ── ② 把最新的身份信息合并回保留的那一行 ──
--
-- 重复行里通常最后一条才是最新的（每次登录插一条）。
-- 直接丢掉的话，用户这次登录算出来的新角色也跟着丢了 ——
-- 表现是「修完之后还要再登一次才生效」，没必要让人多走一趟。
UPDATE users k
  JOIN (
    SELECT username, MAX(id) AS latest_id
      FROM users WHERE deleted_at IS NOT NULL AND auth_source = 'sso'
     GROUP BY username
  ) x ON x.username = k.username
  JOIN users l ON l.id = x.latest_id
   SET k.role_code    = l.role_code,
       k.oidc_groups  = l.oidc_groups,
       k.oidc_subject = l.oidc_subject,
       k.display_name = COALESCE(NULLIF(l.display_name, ''), k.display_name),
       k.email        = COALESCE(NULLIF(l.email, ''), k.email)
 WHERE k.deleted_at IS NULL AND k.auth_source = 'sso' AND l.id > k.id;

-- ── ③ 生成列 + 真正生效的唯一索引 ──
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND COLUMN_NAME='alive_username'),
  'DO 0',
  'ALTER TABLE users
     ADD COLUMN alive_username VARCHAR(64)
       GENERATED ALWAYS AS (IF(deleted_at IS NULL, username, NULL)) STORED');
PREPARE st FROM @s;
EXECUTE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND INDEX_NAME='uk_alive_username'),
  'DO 0',
  'ALTER TABLE users ADD UNIQUE KEY uk_alive_username (alive_username)');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;
