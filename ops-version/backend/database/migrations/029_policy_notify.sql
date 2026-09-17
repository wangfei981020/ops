-- 029 「哪些复制规则要发通知」由人来选，不再由触发方式推断
--
-- # 为什么换判据
--
-- 原来发不发由 notify.ShouldNotify 按「触发方式 + 成败」推断：
-- 失败一律发、手动成功发、自动成功不发。那套规则是用**少发**来防刷屏的，
-- 代价是两头不讨好：
--
--   · 你不关心的规则（别的团队手动点一次同步）照样发到你群里；
--   · 你关心的规则自动触发成功时反而不发。
--
-- 现在改成白名单：**这条规则开了通知，就成功失败都发；没开，一条都不发。**
-- 防刷屏由「只选几条规则」来承担 —— 白名单比按触发方式分级精确得多。
--
-- 🔴 默认 0（不通知）。上线后在界面上勾选之前，一条通知都不会发出去，
--    **包括失败通知**。这是用户明确要的取舍，不是疏漏。
--
-- ⚠️ sync_policies 的行由采集器每轮从 Harbor **覆盖写回**（store.SavePolicies），
--    所以 notify_enabled 绝不能出现在那条 ON DUPLICATE KEY UPDATE 里 ——
--    出现的话，人勾的开关会在下一轮采集（最多 30 分钟）被静默重置成 0，
--    而且不报错、日志里也看不出来。org_id（绑定平台）就是这么处理的，照它来。

SET @c := (SELECT COUNT(*) FROM information_schema.COLUMNS
            WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sync_policies' AND COLUMN_NAME = 'notify_enabled');
SET @s := IF(@c = 0,
  'ALTER TABLE sync_policies ADD COLUMN notify_enabled TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''这条规则要不要发通知；1=成功与失败都发，0=一条都不发。人工设置，采集器不得覆盖'' AFTER org_id',
  'DO 0');
-- ⚠️ 三条各占一行（执行器按行尾分号切；写一行 Error 1064，mysql CLI 验不出来）
PREPARE stmt FROM @s;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- # 去重用的两列
--
-- 🔴 改成「成功也发」之后，同一次复制会被通知**两次**：
--    webhook 收到事件实时发一张，30 分钟后采集器轮询发现这条 execution
--    是新的、状态有变化，又发一张一模一样的。
--
--    原来不会撞车，纯属巧合：webhook 那条路成功也发，而采集器那条路
--    按分级规则「自动触发且成功」不发 —— 两条路正好错开。现在分级没了，
--    错开的前提也没了。
--
--    去重键是 (policy_ref, harbor_exec_id)：同一条规则的同一次 execution
--    只要已经**成功发出去过**（state='sent'），采集器就不再发，落一条
--    skipped 说明「webhook 已实时通知」。
--
-- ⚠️ 不能拿 exec_ref（sync_executions.id）当键：webhook 到达时那一行还不存在
--    （sync_executions 由采集器写），只有 Harbor 的 execution id 两条路都拿得到。

SET @c := (SELECT COUNT(*) FROM information_schema.COLUMNS
            WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'notify_records' AND COLUMN_NAME = 'policy_ref');
SET @s := IF(@c = 0,
  'ALTER TABLE notify_records ADD COLUMN policy_ref BIGINT UNSIGNED NULL COMMENT ''sync_policies.id；哪条复制规则'' AFTER exec_ref',
  'DO 0');
PREPARE stmt FROM @s;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @c := (SELECT COUNT(*) FROM information_schema.COLUMNS
            WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'notify_records' AND COLUMN_NAME = 'harbor_exec_id');
SET @s := IF(@c = 0,
  'ALTER TABLE notify_records ADD COLUMN harbor_exec_id BIGINT NOT NULL DEFAULT 0 COMMENT ''Harbor 的 execution id；0=拿不到（老记录、渠道测试消息）'' AFTER policy_ref',
  'DO 0');
PREPARE stmt FROM @s;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 去重查询是 WHERE policy_ref=? AND harbor_exec_id=? AND state='sent'
SET @c := (SELECT COUNT(*) FROM information_schema.STATISTICS
            WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'notify_records' AND INDEX_NAME = 'idx_dedup');
SET @s := IF(@c = 0,
  'CREATE INDEX idx_dedup ON notify_records (policy_ref, harbor_exec_id, state)',
  'DO 0');
PREPARE stmt FROM @s;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
