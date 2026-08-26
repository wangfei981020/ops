-- 026 记下 webhook 最近一次收到了什么
--
-- 🔴 webhook 这类东西最难查的地方是**看不见**：
--
--    Harbor 那边显示推送成功、我们这边令牌的「最近调用」也在更新，
--    可就是没有数据落库 —— 而两边都显示正常。
--    真正的原因藏在后端日志里（策略名对不上 / artifact 为空 / 事件类型不对），
--    而运维多半没有生产日志的权限，只能来问开发。
--
--    实测过（2026-08-25）就卡在这：令牌 last_used_at 一直在更新，
--    148 条复制记录里带版本号的仍然是 0 条，从界面上完全看不出为什么。
--
-- ⚠️ 只存**摘要**不存原始 payload：payload 里有 Harbor 主机名、
--    仓库路径这些信息，长期留在库里没必要，而摘要足够回答「为什么没落库」。

SET @c := (SELECT COUNT(*) FROM information_schema.COLUMNS
            WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'webhook_tokens' AND COLUMN_NAME = 'last_event');
SET @s := IF(@c = 0,
  'ALTER TABLE webhook_tokens ADD COLUMN last_event VARCHAR(500) NOT NULL DEFAULT '''' COMMENT ''最近一次收到的事件摘要：事件类型/策略名/artifact数/落库数''',
  'DO 0');
-- ⚠️ 三条各占一行（执行器按行尾分号切；写一行会 Error 1064，而 mysql CLI 验不出来）
PREPARE stmt FROM @s;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
