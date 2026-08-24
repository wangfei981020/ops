-- OpsVersion 004 · Harbor 镜像同步
--
-- 目的不是"多一个页面"，而是给对账补上**因果链**：
-- 同样是「对方版本落后」，可能是我们这边镜像没同步过去（我们的锅），
-- 也可能是同步了对方没发版（对方的节奏）。没有这层数据，两者在表上长得一模一样。

CREATE TABLE IF NOT EXISTS sync_policies (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  harbor_id     BIGINT UNSIGNED NOT NULL COMMENT '源 Harbor 配置 id',
  policy_id     BIGINT       NOT NULL COMMENT 'Harbor 里的 replication policy id',
  name          VARCHAR(128) NOT NULL,
  dest_registry VARCHAR(255) NOT NULL DEFAULT '' COMMENT '目标 Harbor',
  -- 绑到哪个实例。绑了才能把同步状态并进那一列的对账结果
  instance_id   BIGINT UNSIGNED NULL,
  trigger_type  VARCHAR(32)  NOT NULL DEFAULT '' COMMENT 'manual|event_based|scheduled',
  enabled       TINYINT(1)   NOT NULL DEFAULT 1,
  updated_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_policy (harbor_id, policy_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Harbor 复制规则';

CREATE TABLE IF NOT EXISTS sync_executions (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  policy_ref    BIGINT UNSIGNED NOT NULL COMMENT 'sync_policies.id',
  exec_id       BIGINT       NOT NULL COMMENT 'Harbor 的 execution id',
  -- 🔴 Harbor 各版本取值不统一，三种拼法都要认（照搬现有脚本踩出来的）：
  --    manual / event·event_based·event-based / scheduled·schedule·cron
  trigger_type  VARCHAR(32)  NOT NULL DEFAULT '',
  status        VARCHAR(32)  NOT NULL DEFAULT '' COMMENT 'Succeeded|Failed|InProgress|Stopped',
  total         INT          NOT NULL DEFAULT 0,
  succeeded     INT          NOT NULL DEFAULT 0,
  failed        INT          NOT NULL DEFAULT 0,
  started_at    DATETIME     NULL,
  ended_at      DATETIME     NULL,
  synced_at     DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '我们拉到这条记录的时刻',
  PRIMARY KEY (id),
  UNIQUE KEY uk_exec (policy_ref, exec_id),
  KEY idx_time (started_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='复制执行记录';

-- 每个镜像的同步明细。这一层才有「某个服务的某个 tag 同步成功没有」的粒度
CREATE TABLE IF NOT EXISTS sync_tasks (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  policy_ref    BIGINT UNSIGNED NOT NULL,
  exec_id       BIGINT       NOT NULL,
  -- 🔴 入库前必须剥掉 Harbor 返回的 ` [3 item(s) in total]` 尾巴，
  --    不剥会把同一个服务算成两个（查文档查不到，只有真跑过才知道）
  service_key   VARCHAR(191) NOT NULL COMMENT '镜像名最后一段，与 service_versions 同一套 key',
  tag           VARCHAR(191) NOT NULL DEFAULT '',
  status        VARCHAR(32)  NOT NULL DEFAULT '' COMMENT 'Succeed|Failed|InProgress|Stopped',
  err_msg       VARCHAR(500) NOT NULL DEFAULT '',
  finished_at   DATETIME     NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_task (policy_ref, exec_id, service_key, tag),
  KEY idx_svc (service_key, tag),
  KEY idx_time (finished_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='单个镜像的同步结果';

-- Harbor 连接配置
CREATE TABLE IF NOT EXISTS harbors (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name          VARCHAR(64)  NOT NULL,
  endpoint      VARCHAR(255) NOT NULL COMMENT '如 https://harbor.example.com',
  username      VARCHAR(128) NOT NULL DEFAULT '' COMMENT 'robot 账号',
  credential_enc TEXT        NULL COMMENT '加密存储，永不回显',
  insecure_tls  TINYINT(1)   NOT NULL DEFAULT 0,
  enabled       TINYINT(1)   NOT NULL DEFAULT 1,
  last_sync_at  DATETIME     NULL,
  last_sync_status VARCHAR(32) NOT NULL DEFAULT 'never',
  last_sync_error  VARCHAR(500) NOT NULL DEFAULT '',
  created_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deleted_at    DATETIME     NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_name (name, deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='源 Harbor（我们往外推的那一侧）';
