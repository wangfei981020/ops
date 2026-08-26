-- 022 平台级采集范围：让「平台级」这一列有自己的口径，不再被项目级规则挖洞
--
-- 🔴 要解决的问题：
--
--    项目的 workload_exclude 现在同时干了两件事 —— 「不要采」和「不要比」。
--    于是项目级的口径决定了平台级能看到什么：
--
--      我方·项目A   ns=app-*    排除 *-game-frontend / *-game-server-backend / *-resource-backend / biz-*
--      我方·项目B ns=biz-uat  无排除
--
--    那 67 个游戏服务在 app-* 里被 项目A 项目排掉，又不属于 项目B（它只收 biz-uat）——
--    **没有任何一个项目负责收它们**，于是 我方 整个平台都看不见。
--    而对面 A公司 采到了，对账时这 21 行只能显示「已忽略」，根本没比。
--
--    org.go 的注释里早就写明了正确的分工：
--      「采集要全（数据留着以后能查），比对才收窄」
--    这张表就是把采集那一半交还给平台。
--
-- ⚠️ 为什么另起一张表，而不是往 org_envs 塞一行 project_id=0：
--    org_envs 一行 = 一个项目，带着 endpoint、凭据、上次采集状态。
--    塞一行进去，现有每一处遍历 in.Envs 的代码都得记得「跳过那一行」——
--    漏一处就是把平台级配置当成一个项目去采、去显示、去算新鲜度。
--
-- ⚠️ 只放范围，不放 workload 规则：平台级**故意**不做 workload 排除。
--    平台级要的就是全量；要收窄请用方案里的忽略规则（那个两边对称，
--    不会造成"一边排了一边没排"的假缺失）。

CREATE TABLE IF NOT EXISTS org_env_scopes (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  org_id       BIGINT UNSIGNED NOT NULL,
  env          VARCHAR(32)  NOT NULL,

  -- 换行分隔，与 org_envs 的同名字段同一套写法（支持 * 通配）
  ns_include   TEXT NULL COMMENT '平台级采集的 ns 范围。空 = 不启用平台级采集',
  ns_exclude   TEXT NULL,

  -- 平台级采哪些集群。留空 = 沿用该环境下项目行里的集群
  -- （绝大多数情况两者相同，留空能少配一处、少一处配错）
  cluster_refs TEXT NULL,

  -- 🔴 显式开关，而不是「配了 ns 就算启用」：
  --    ns 填错导致零结果时，人需要能一眼分清"没开"和"开了但没采到"。
  enabled      TINYINT(1)   NOT NULL DEFAULT 0,

  -- 平台级采集自己的状态。不能并进 org_envs 的项目行 ——
  -- 那样"平台级采失败"会显示成某个项目采失败，找错方向。
  last_collect_at             DATETIME     NULL,
  last_collect_status         VARCHAR(32)  NOT NULL DEFAULT '',
  last_collect_error          VARCHAR(500) NOT NULL DEFAULT '',
  last_collect_degraded       TINYINT(1)   NOT NULL DEFAULT 0,
  last_collect_degraded_note  VARCHAR(255) NOT NULL DEFAULT '',

  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_org_env (org_id, env)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='平台级采集范围（一个平台×环境一行，采全量给平台级视图用）';
