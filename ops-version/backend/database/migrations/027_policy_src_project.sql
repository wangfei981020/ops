-- 027 复制规则记下「从哪个源项目复制」
--
-- 🔴 为什么需要它：Harbor 的 webhook payload 里**没有规则名**
--    （description 是"描述"字段，用户可以不填，生产上就是空的）。
--    退而求其次按目标 Harbor 地址匹配，可生产上 appA / bizB / monitoring
--    三条规则推的是同一个 Harbor —— 匹配必然命中多条，只能取第一条，
--    于是 appA 推的镜像被记成了 bizB 推的。
--
--    规则的 filters 里有源项目（`appA/**`），而 payload 里有 dest_ns/src_ns，
--    两者一对就能精确定位。这是唯一可靠的线索。
--
-- ⚠️ 允许为空：没设名称过滤的规则（全量复制）本来就没有源项目，
--    那时仍退回按目标地址匹配 —— 不能因为拿不到就整条不匹配。

SET @c := (SELECT COUNT(*) FROM information_schema.COLUMNS
            WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sync_policies' AND COLUMN_NAME = 'src_project');
SET @s := IF(@c = 0,
  'ALTER TABLE sync_policies ADD COLUMN src_project VARCHAR(128) NOT NULL DEFAULT '''' COMMENT ''源项目名，取自 Harbor 规则 filters 的 name（appA/** → appA）；空=该规则没设名称过滤'' AFTER dest_registry',
  'DO 0');
-- ⚠️ 三条各占一行（执行器按行尾分号切；写一行 Error 1064，mysql CLI 验不出来）
PREPARE stmt FROM @s;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
