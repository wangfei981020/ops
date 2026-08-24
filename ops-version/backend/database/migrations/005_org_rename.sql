-- OpsVersion 005 · 术语统一：实例 → 组织（org）
--
-- 为什么改：「实例」是运维黑话，在本产品里它代表的其实是「一个被对账的组织」——
-- 我方、A公司、B公司。一个组织可能有多个接入点（客户 UAT 一个 Rancher、PROD 一个），
-- 这由 org_envs 的连接覆盖解决，不需要靠建多条记录来表达，所以「组织」是准确的。
--
-- 🔴 本文件用「条件 DDL」写法（SET @s := IF(EXISTS(...))+PREPARE+EXECUTE）而不是裸 DDL：
--    RENAME TABLE / RENAME COLUMN 重跑时报的是 1146/1054，
--    不在 runner 的 isAlreadyExistsErr 白名单里，中断后重启会卡死在这一支。
--    把 1146 加进白名单是错的做法 —— 那会连真正的「表不存在」也一起吞掉。
--    ⚠️ 这种写法要求所有语句跑在同一条连接上（会话变量 + 语句句柄都是连接级的），
--       runner.Run 已改为 db.Conn 单连接执行。

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='instances'), 'RENAME TABLE instances TO orgs', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='instance_envs'), 'RENAME TABLE instance_envs TO org_envs', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='org_envs' AND COLUMN_NAME='instance_id'), 'ALTER TABLE org_envs RENAME COLUMN instance_id TO org_id', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='service_versions' AND COLUMN_NAME='instance_id'), 'ALTER TABLE service_versions RENAME COLUMN instance_id TO org_id', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='version_changes' AND COLUMN_NAME='instance_id'), 'ALTER TABLE version_changes RENAME COLUMN instance_id TO org_id', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='service_aliases' AND COLUMN_NAME='instance_id'), 'ALTER TABLE service_aliases RENAME COLUMN instance_id TO org_id', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='sync_policies' AND COLUMN_NAME='instance_id'), 'ALTER TABLE sync_policies RENAME COLUMN instance_id TO org_id', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND COLUMN_NAME='visible_instances'), 'ALTER TABLE users RENAME COLUMN visible_instances TO visible_orgs', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='mcp_tokens' AND COLUMN_NAME='visible_instances'), 'ALTER TABLE mcp_tokens RENAME COLUMN visible_instances TO visible_orgs', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='orgs' AND INDEX_NAME='uk_instance_name'), 'ALTER TABLE orgs RENAME INDEX uk_instance_name TO uk_org_name', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='org_envs' AND INDEX_NAME='uk_inst_env'), 'ALTER TABLE org_envs RENAME INDEX uk_inst_env TO uk_org_env', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='service_versions' AND INDEX_NAME='uk_inst_env_svc'), 'ALTER TABLE service_versions RENAME INDEX uk_inst_env_svc TO uk_org_env_svc', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='service_aliases' AND INDEX_NAME='uk_inst_alias'), 'ALTER TABLE service_aliases RENAME INDEX uk_inst_alias TO uk_org_alias', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;

DEALLOCATE PREPARE st;

UPDATE comparison_plans SET columns_json = REPLACE(columns_json, '"instance_id"', '"org_id"') WHERE columns_json LIKE '%"instance_id"%';
UPDATE comparison_plans SET baseline_json = REPLACE(baseline_json, '"instance_id"', '"org_id"') WHERE baseline_json LIKE '%"instance_id"%';
UPDATE audit_logs SET action = CONCAT('org.', SUBSTRING(action, 10)) WHERE action LIKE 'instance.%';

ALTER TABLE orgs COMMENT='对账组织（我方 + 各客户公司）';
ALTER TABLE org_envs COMMENT='组织的环境映射与 ns 规则';
