-- OpsVersion 007 · Pod 级明细
--
-- 为什么要存到 Pod 这一层（workload 级的一个副本数不够用）：
-- 排查「版本改了但没生效」时要看的是**各副本各自的状态** ——
-- 3 个副本里 2 个跑新版 1 个卡在旧版、某个副本重启了 47 次、
-- 某个节点上的副本一直起不来，这些在 workload 级完全看不见。
--
-- ⚠️ 这些数据**本来就已经拉到了**：算 running_tag 时打的就是 pod 接口，
-- 过去只取了 containerStatuses 的 image/imageID。多存这一份不增加任何网络请求。
--
-- 🔴 与 service_versions 一样是**全量覆盖写**：每次采集先删该 (org,env) 的旧行再插。
-- 增量合并会让已经被删除的 Pod 永远留在表里，导出时显示成「还在跑」。
CREATE TABLE IF NOT EXISTS service_pods (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  org_id       BIGINT UNSIGNED NOT NULL,
  env          VARCHAR(32)  NOT NULL,

  service_key  VARCHAR(191) NOT NULL COMMENT '镜像名最后一段，与 service_versions 对得上',
  namespace    VARCHAR(128) NOT NULL DEFAULT '',
  pod_name     VARCHAR(253) NOT NULL DEFAULT '',
  container    VARCHAR(128) NOT NULL DEFAULT '',

  image_repo   VARCHAR(512) NOT NULL DEFAULT '' COMMENT '完整镜像串，导出的 Image 列直出',
  tag          VARCHAR(191) NOT NULL DEFAULT '',

  -- 🔴 phase 与 ready 严格分开：Phase=Running 但 Ready=false 是最常见的故障态
  --    （探针一直不过）。只看 phase 会把它显示成健康。
  phase        VARCHAR(32)  NOT NULL DEFAULT '',
  ready        TINYINT(1)   NOT NULL DEFAULT 0,
  restarts     INT          NOT NULL DEFAULT 0,

  pod_ip       VARCHAR(64)  NOT NULL DEFAULT '',
  node         VARCHAR(253) NOT NULL DEFAULT '',

  -- 存启动时刻而不是「跑了多久」：后者一落库就开始骗人，
  -- 导出时算出来的会是采集那一刻的年龄而不是导出时的。
  -- NULL = 解析不出，导出显示「—」，不拿 now() 兜底（那会让老 Pod 看着刚起来）。
  started_at   DATETIME     NULL,

  observed_at  DATETIME     NOT NULL,
  PRIMARY KEY (id),
  KEY idx_org_env (org_id, env),
  KEY idx_svc (org_id, env, service_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Pod 级运行时明细（全量覆盖写）';
