-- OpsVersion 009 · 三件事：术语合并为「平台」、采集层 workload 过滤、比对层服务白名单
--
-- 1) 删 product：原来是「组织 × 平台」两层（我方 × appA）。实际用法里一条记录
--    就是一套部署（我方 / A公司 / A公司-PROD），第二层从没派上用场，留着只是让人多填一栏。
--    ⚠️ 这是**不可逆**的：删了就没地方表达「同一套产品交付给多个客户」。
--    现在删是代价最小的时刻 —— 还没有真实数据依赖它。
--
-- 2) org_envs 加 workload 规则：ns 规则太粗。一个 ns 里几十个服务，
--    而各家部署的服务集合并不相同（我方 UAT 有的，对方可能压根没有）。
--
-- 3) comparison_plans 加 service_include：与 workload 规则**不是一回事**，
--    前者管「这次比哪些」，后者管「采什么回来」。
--    分两层是因为：采集要全（数据留着以后能查），而比对时人只关心那几个。

SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='orgs' AND COLUMN_NAME='product'), 'ALTER TABLE orgs DROP COLUMN product', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='comparison_plans' AND COLUMN_NAME='product'), 'ALTER TABLE comparison_plans DROP COLUMN product', 'DO 0');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

ALTER TABLE orgs COMMENT='对比平台（我方与各客户的部署，一条记录 = 一套部署）';

-- 采集层：只抄这些 workload 回来。留空 = 该 ns 下全部。
-- 🔴 与 ns 规则同一层，都是「采什么」，各平台各配各的 ——
--    因为对方的 ns 划分和服务集合我们控制不了，也不需要跟我方一致。
ALTER TABLE org_envs
  ADD COLUMN workload_include TEXT NULL COMMENT '换行分隔，支持 * 前缀。留空=该 ns 下全部' AFTER ns_exclude,
  ADD COLUMN workload_exclude TEXT NULL COMMENT '换行分隔，优先级高于 include' AFTER workload_include;

-- 比对层：这次只比这些服务。留空 = 全部。
-- 🔴 按**服务名**（镜像名最后一段）而不是 deployment 名：
--    服务名是各平台唯一对得齐的东西，一份配置对所有平台生效；
--    按 deployment 名的话，各家命名不同就要配 N 份，而且对方改个名这里就静默失效。
ALTER TABLE comparison_plans
  ADD COLUMN service_include TEXT NULL COMMENT '换行分隔，支持 * 前缀。留空=全部服务' AFTER kinds;
