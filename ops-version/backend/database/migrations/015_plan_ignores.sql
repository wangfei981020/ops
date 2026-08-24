-- 方案里存忽略规则。
--
-- 背景：对账表上总有一些"本来就不该比"的格子 ——
--   对方压根不跑这套服务（整行不比）
--   只有某一家不跑（那一格不比）
-- 不忽略的话，这些格子会一直显示成「该列没有」，
-- 和真正的"漏部署"混在一起，久而久之整张表没人看。
--
-- 🔴 存进方案而不是全局：不同客户用不同方案，排除项也不同。
-- ⚠️ 必须可解除 —— 对方以后可能上线这个服务，那时不该逼人重建方案。
--    所以是一份可编辑的规则，不是"点一下就永久消失"。

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='comparison_plans' AND COLUMN_NAME='ignores_json'),
  'DO 0',
  'ALTER TABLE comparison_plans ADD COLUMN ignores_json TEXT NULL COMMENT ''忽略规则 {"services":[],"cells":{}}；cells 的列标识是 StableKey(orgID/projectID/env)''');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

-- ⚠️ 不做数据回填：ignores_json 为 NULL 就是「没有忽略规则」，
--    读出来解析成空 IgnoreSet 即可。写一个 '{}' 进去反而多一种状态。
