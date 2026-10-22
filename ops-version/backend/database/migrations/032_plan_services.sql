-- 方案里存「只比这些服务」。
--
-- 背景：版本比对页的服务选择从手打文本框改成了下拉多选，
-- 选的往往是固定的那十几个（某条业务线的服务），每次套用方案都重选一遍没有意义。
--
-- 🔴 存的是服务名（镜像名最后一段），不是通配：界面上只能从已采集的服务里选。
-- ⚠️ NULL / 空数组 = 比全部服务，与不存这一列时的老方案语义一致，不做回填。

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='comparison_plans' AND COLUMN_NAME='services_json'),
  'DO 0',
  'ALTER TABLE comparison_plans ADD COLUMN services_json TEXT NULL COMMENT ''只比这些服务 ["svc-a","svc-b"]；空 = 全部''');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;
