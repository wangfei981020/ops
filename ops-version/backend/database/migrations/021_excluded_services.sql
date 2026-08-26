-- 021 记下「这一列有哪些服务是被采集规则主动排掉的」
--
-- 🔴 为什么要存这份事实，而不是下游各自拿规则反推：
--
--    规则匹配的是 **workload 名**，而预检和对账认的是 **ServiceKey（镜像名最后一段）**。
--    两者在 helm 部署下基本对不上 —— helm 会把 release 名拼进 workload 名：
--
--        workload  opsalert-另一个产品-backend      ← 规则 另一个产品-* 匹配不上
--        镜像       .../另一个产品-backend:v0.9.8    ← ServiceKey 是 另一个产品-backend，匹配得上
--
--    于是「拿规则去比 ServiceKey」得出的结论，与实际被排掉的服务**没有交集**：
--    预检报「命中 另一个产品-backend」，而真正被排掉的是另外两个。
--
--    只有在过滤发生的那一刻同时记下两个名字，下游拿到的才是事实而不是猜测。
--
-- ⚠️ 只记名字，不记版本：这些服务没有参与对账的资格，
--    存版本会让人以为这是一份"可用数据"，迟早有人拿它去比。
--
-- ⚠️ 与 service_versions 分表而不是加一个 is_excluded 列：
--    加列的话，现有每一处查询都得记得加 `AND is_excluded=0` ——
--    漏一处就是把"我们主动不看的服务"当成真实数据参与了对账，
--    而那种错静默、且只在配了排除规则的列上出现。

CREATE TABLE IF NOT EXISTS excluded_services (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  org_id      BIGINT UNSIGNED NOT NULL,
  -- 一列 = 平台 × 项目 × 环境。少了 project_id 会跨项目串数据（同 service_versions）
  project_id  BIGINT UNSIGNED NOT NULL DEFAULT 0,
  env         VARCHAR(32)  NOT NULL,
  service_key VARCHAR(191) NOT NULL COMMENT '镜像名最后一段——对账与预检认的就是它',
  workload    VARCHAR(191) NOT NULL COMMENT '规则实际匹配的那个名字，排查时两个都要在',
  namespace   VARCHAR(191) NOT NULL DEFAULT '',
  observed_at DATETIME     NOT NULL,
  PRIMARY KEY (id),
  -- 同一个 workload 下多个容器可能产出同名 ServiceKey，用三元组去重
  UNIQUE KEY uk_excluded (org_id, project_id, env, service_key, workload),
  KEY idx_col (org_id, project_id, env)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='被采集规则排除的服务清单（只存名字，不存版本）';
