-- 025 环境行可以直接引用数据源
--
-- 🔴 解决的问题：客户的 UAT 和 PROD 常常是**两套独立的 Rancher**，
--    而环境级原来只能「手填地址 + 手填账号密码」。
--    于是同一套 PROD Rancher 的凭据，有几个项目就要各填一遍，
--    改一次密码要改 N 处 —— 漏掉一处的表现是那一列采集失败，
--    而失败原因写的是「认证失败」，人会去查账号对不对，查不到是"另一处没改"。
--
--    数据源这一层本来就是为「同一个 Rancher 被多处共用」而存在的，
--    只是原来只有**平台**能引用它，环境不能。这条迁移把它补上。
--
-- ⚠️ 与「环境级手填」并存，不删旧字段：
--    已经手填过的环境行还在正常工作，删了它们立刻全断。
--    优先级见 OrgEnv.Conn()：环境数据源 > 环境手填 > 平台手填 > 平台数据源。

SET @c := (SELECT COUNT(*) FROM information_schema.COLUMNS
            WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'org_envs' AND COLUMN_NAME = 'datasource_id');
SET @s := IF(@c = 0,
  'ALTER TABLE org_envs ADD COLUMN datasource_id BIGINT UNSIGNED NULL COMMENT ''引用 datasources.id；空=沿用下一层（见 OrgEnv.Conn）'' AFTER credential_enc',
  'DO 0');
-- ⚠️ 这三条必须各占一行（执行器按行尾分号切语句；写一行会 Error 1064，
--    而且 mysql CLI 验不出来 —— 见 017/024 的同款注释）
PREPARE stmt FROM @s;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
