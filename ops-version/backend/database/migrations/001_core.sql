-- OpsVersion 001 · 核心模型：实例 / 环境映射 / 版本快照 / 变更历史 / 别名 / 对比方案
--
-- ⚠️ 术语已在 005 统一为「组织(org)」：instances→orgs、instance_envs→org_envs、
--    instance_id→org_id、visible_instances→visible_orgs。
--    本文件**保留历史原貌** —— 迁移记录的是演进过程，回改已应用的迁移会让
--    「从零建库」和「增量升级」走出两条不同的路径。当前 schema 以 005 为准。
--
-- 设计依据（详见 docs/plans/opsversion-plan.md §1）：
--   对账 key = 镜像名最后一段。registry host、Harbor 项目名、namespace、workload 名
--   四者双方都可能不一致，全部剥掉，只留镜像名 + tag。

-- ─────────────────────────────────────────────────────────────
-- 实例：我方也是一个普通实例，不是特例分支
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS instances (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name            VARCHAR(64)  NOT NULL                COMMENT '实例名，如 我方 / A公司',
  product         VARCHAR(64)  NOT NULL DEFAULT ''     COMMENT '产品线，如 appA 平台',

  -- 数据来源。四种 provider 是同一个接口的不同实现，我方走 kite 也只是其中一种
  provider_type   VARCHAR(32)  NOT NULL                COMMENT 'kite | rancher | kubeconfig | manual_import',

  -- ⚠️ 下面三个是**实例级默认值**，可被 instance_envs 同名字段覆盖（见该表说明）。
  --    我方：一个 Kite 接多个集群 → 这里配一次，四个环境共用，各环境只填不同的 cluster_refs。
  --    客户：一个公司可能有两个 Rancher（UAT 一个、PROD 一个）→ 这里留空，各环境各配各的。
  -- ⚠️ Kite 与 Rancher 的认证头不同：Kite 只认 Cookie(auth_token=<JWT>)，Bearer 返回 401；
  --    Rancher 用 Authorization: Bearer。provider 必须把「认证头怎么带」也抽象掉。
  auth_type       VARCHAR(32)  NOT NULL DEFAULT ''     COMMENT 'password | api_key | kubeconfig | none',
  endpoint        VARCHAR(255) NOT NULL DEFAULT ''     COMMENT 'Kite / Rancher 地址（默认值）',
  -- 凭据加密存储，任何角色任何接口都不回显明文（含超级管理员）
  credential_enc  TEXT         NULL                    COMMENT 'AES 加密的凭据 JSON，永不回显',

  -- 下面两个纯展示，**不参与比对** —— 客户那边可能跟我方完全不同
  harbor_host     VARCHAR(255) NOT NULL DEFAULT ''     COMMENT '仅展示',
  harbor_project  VARCHAR(128) NOT NULL DEFAULT ''     COMMENT '仅展示，客户可能与我方不同',

  is_self         TINYINT(1)   NOT NULL DEFAULT 0      COMMENT '1=我方',
  enabled         TINYINT(1)   NOT NULL DEFAULT 1,

  -- 🔴 采集结果必须是多态，不能只有「成功/失败」两态。
  --    认证失败若退化成「返回空列表」，界面会显示成「该实例没有任何服务」，
  --    而这跟「对方真的下线了所有服务」在表上长得一模一样。
  last_sync_at     DATETIME    NULL,
  last_sync_status VARCHAR(32) NOT NULL DEFAULT 'never'
                   COMMENT 'never | success | auth_failed | unreachable | forbidden | partial | error',
  last_sync_error  VARCHAR(500) NOT NULL DEFAULT '',

  created_at      DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at      DATETIME     NULL,
  PRIMARY KEY (id),
  -- ⚠️ 唯一索引带 deleted_at：软删除后同名可重建。
  --    只写 (name) 会让删掉的实例永久占用名字。
  UNIQUE KEY uk_instance_name (name, deleted_at),
  KEY idx_enabled (enabled, deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='部署实例（我方 + 各客户公司）';

-- ─────────────────────────────────────────────────────────────
-- 实例 × 环境 → 去哪找。namespace 规则每个实例各配各的，互不影响
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS instance_envs (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  instance_id  BIGINT UNSIGNED NOT NULL,
  env          VARCHAR(32)  NOT NULL                COMMENT 'DEV | TEST | UAT | PROD，可扩展',

  -- 🔴 换行分隔，**支持一个环境跨多个集群**。
  --    Kite：填 cluster name（一个 Kite 接多个集群，各环境填各自那个）
  --    Rancher：填 clusterId（c-m-xxxx）
  cluster_refs TEXT         NULL                    COMMENT '换行分隔，一行一个集群',

  -- 🔴 连接覆盖：留空则继承 instances 上的同名字段。
  --    我方 Kite 一个 endpoint 打多个集群 → 这三个全留空，只填 cluster_refs。
  --    客户两个 Rancher（UAT 一个 PROD 一个）→ 每个环境各填各的 endpoint + 凭据。
  --    做成「默认 + 覆盖」而不是一律环境级，是因为绝大多数实例只有一个 endpoint，
  --    强制每个环境重填一遍既啰嗦又容易配错（改了一个忘了另一个，表现为半边数据是旧的）。
  endpoint     VARCHAR(255) NOT NULL DEFAULT ''     COMMENT '空=继承实例级',
  auth_type    VARCHAR(32)  NOT NULL DEFAULT ''     COMMENT '空=继承实例级',
  credential_enc TEXT       NULL                    COMMENT '空=继承实例级；加密存储，永不回显',

  -- 换行分隔，支持 * 前缀匹配（app-*）。
  -- ⚠️ 必须同时有 exclude：app-* 会把 app-uat 和 app-prod 一起抓进来，
  --    导致同一个镜像名命中两个 workload → 同名冲突（见 service_versions.conflict_detail）
  ns_include   TEXT         NULL                    COMMENT '换行分隔，支持 * 前缀匹配',
  ns_exclude   TEXT         NULL                    COMMENT '换行分隔，优先级高于 include',

  -- 0 = 只采集展示、不参与一致性判定。
  -- DEV/TEST 必须设 0：与 UAT/PROD 是两个 Harbor 两条独立流水线，
  -- 构建号不同序列，横向比数字就是错的。
  compare_enabled TINYINT(1) NOT NULL DEFAULT 1,

  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_inst_env (instance_id, env)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='实例的环境映射与 ns 规则';

-- ─────────────────────────────────────────────────────────────
-- 版本快照：一行 = 一个 (实例, 环境, 服务) 当前跑的版本
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS service_versions (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  instance_id  BIGINT UNSIGNED NOT NULL,
  env          VARCHAR(32)  NOT NULL,

  -- 🔴 对账 key：镜像名最后一段。入库前必须清洗掉 Harbor 返回的
  --    ` [3 item(s) in total]` 尾巴，否则同一个服务会被算成两个。
  service_key  VARCHAR(191) NOT NULL                COMMENT '镜像名最后一段',
  image_repo   VARCHAR(255) NOT NULL DEFAULT ''     COMMENT '完整仓库路径，仅展示',

  -- 🔴 tag 与 running_tag 分开存：
  --    tag         来自 deployment.spec ── 「声明要跑什么」
  --    running_tag 来自 pod.status.containerStatuses[].imageID ── 「实际在跑什么」
  --    两者不一致 = 正在滚动更新，或滚动卡住了。
  --    只看 deployment 会把「YAML 改了但一个 pod 都没起来」显示成「已升级」——会骗人的绿灯。
  tag          VARCHAR(191) NOT NULL DEFAULT '',
  running_tag  VARCHAR(191) NOT NULL DEFAULT '',
  digest       VARCHAR(191) NOT NULL DEFAULT ''     COMMENT '可选，需额外打 pod 接口才有',

  -- 从 tag 末段解析出的构建号。解析不出为 NULL —— 此时只能判「相同/不同」，不能算落后几个版本
  build_no     INT          NULL,
  -- 0 = 非版本化 tag（stable / latest / stable-otel 之类）。
  -- 🔴 这类必须归入「无法判定」，不能因为两边字符串相同就判绿。
  is_versioned TINYINT(1)   NOT NULL DEFAULT 1,

  namespace    VARCHAR(128) NOT NULL DEFAULT ''     COMMENT '各实例可完全不同，仅展示',
  workloads    TEXT         NULL                    COMMENT 'JSON 数组：共用此镜像的 workload 名',
  workload_cnt INT          NOT NULL DEFAULT 0      COMMENT '一个镜像可能被多个 Deployment 共用',

  -- 🔴 非空 = 同名冲突（ns 规则误抓，同一镜像名命中多个 workload 且版本不一致）。
  --    此时**拒绝判定**，绝不静默取第一个 —— 否则看到的是 uat 的版本却以为是 prod 的，
  --    整张表结论全错且看不出来。
  conflict_detail TEXT      NULL                    COMMENT 'JSON：命中的多个 workload 及各自版本',

  observed_at  DATETIME     NOT NULL                COMMENT '采集时刻（判新鲜度用未取整原始值）',
  created_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_inst_env_svc (instance_id, env, service_key),
  KEY idx_svc (service_key),
  KEY idx_observed (observed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='版本快照（全量覆盖写，不做增量）';

-- ─────────────────────────────────────────────────────────────
-- 变更历史：自己攒，不依赖 CMDB 的 workload_changes
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS version_changes (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  instance_id  BIGINT UNSIGNED NOT NULL,
  env          VARCHAR(32)  NOT NULL,
  service_key  VARCHAR(191) NOT NULL,
  old_tag      VARCHAR(191) NOT NULL DEFAULT ''     COMMENT '空 = 首次出现',
  new_tag      VARCHAR(191) NOT NULL DEFAULT ''     COMMENT '空 = 服务消失',
  change_type  VARCHAR(32)  NOT NULL                COMMENT 'upgrade | rollback | added | removed',
  changed_at   DATETIME     NOT NULL,
  created_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_svc_time (instance_id, env, service_key, changed_at),
  KEY idx_time (changed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='版本变更历史（算「落后多少天」的依据）';

-- ─────────────────────────────────────────────────────────────
-- 服务别名：对方改了服务名时防止冒出成对的假缺失
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS service_aliases (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  instance_id  BIGINT UNSIGNED NOT NULL             COMMENT '该别名只对这个实例生效',
  canonical    VARCHAR(191) NOT NULL                COMMENT '标准名（我方的 service_key）',
  alias        VARCHAR(191) NOT NULL                COMMENT '该实例上的实际镜像名',
  note         VARCHAR(255) NOT NULL DEFAULT '',
  created_by   VARCHAR(64)  NOT NULL DEFAULT '',
  created_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_inst_alias (instance_id, alias)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='服务名别名映射';

-- ─────────────────────────────────────────────────────────────
-- 对比方案
-- 🔴 columns 是 (实例, 环境) 的**自由组合**，不能假设「同环境对同环境」——
--    有些项目我方只在 UAT 部署、没有 PROD，需要拿我方 UAT 去比对方的 UAT 和 PROD。
--    一个模型覆盖三种用法：跨公司对账 / 内部晋级检查 / 实例×环境全展开。
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS comparison_plans (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name         VARCHAR(128) NOT NULL,
  product      VARCHAR(64)  NOT NULL DEFAULT '',
  -- JSON 数组，顺序即展示顺序：[{"instance_id":1,"env":"UAT"},{"instance_id":2,"env":"PROD"}]
  columns_json TEXT         NOT NULL,
  -- 基准列，必须是 columns_json 里的某一项：{"instance_id":1,"env":"UAT"}
  baseline_json TEXT        NOT NULL,
  -- 可选：手工版本基线（"这次交付大家都该是 #70"），设了则忽略 baseline_json
  baseline_pin  VARCHAR(191) NOT NULL DEFAULT '',

  kinds        VARCHAR(191) NOT NULL DEFAULT 'Deployment,StatefulSet',
  exclude_workloads TEXT    NULL                    COMMENT '换行分隔，支持 *',
  -- 只对白名单 registry 出来的镜像做判定，nginx/redis 这类公共镜像不参与
  registry_allow TEXT       NULL,
  compare_digest TINYINT(1) NOT NULL DEFAULT 0      COMMENT '1=tag+digest 双重校验（要多打 pod 接口）',

  schedule_cron VARCHAR(64) NOT NULL DEFAULT ''     COMMENT '空 = 仅手动',
  created_by   VARCHAR(64)  NOT NULL DEFAULT '',
  created_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at   DATETIME     NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_plan_name (name, deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='对比方案（列自由组合 + 基准）';
