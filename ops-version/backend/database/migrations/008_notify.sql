-- OpsVersion 008 · 通知渠道与投递记录
--
-- 🔴 为什么投递记录要单独一张表，而不是在 sync_executions 上加一个 notified 字段：
-- 「没发」有好几种原因（规则判定不该发 / webhook 没配 / 发了但失败），
-- 一个布尔字段只能表达「发没发」，回答不了「为什么没发」——
-- 而那正是人来问的时候唯一想知道的事。

CREATE TABLE IF NOT EXISTS notify_channels (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name          VARCHAR(64)  NOT NULL,
  kind          VARCHAR(32)  NOT NULL DEFAULT 'lark' COMMENT '目前只有 lark',
  -- 🔴 webhook 里带 token，等同于凭据 —— 加密存储，接口永不回显。
  --    历史上有脚本把它明文写在 config.py 里，那份文件一进 git 就等于把群送人了。
  webhook_enc   TEXT         NULL,
  enabled       TINYINT(1)   NOT NULL DEFAULT 1,
  -- 只发这个组织的通知。NULL = 全部组织
  org_id        BIGINT UNSIGNED NULL,
  created_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at    DATETIME     NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_chan_name (name, deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='通知渠道';

CREATE TABLE IF NOT EXISTS notify_records (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  channel_id    BIGINT UNSIGNED NULL COMMENT 'NULL = 判定为不发，压根没选渠道',
  exec_ref      BIGINT UNSIGNED NULL COMMENT 'sync_executions.id',

  level         VARCHAR(16)  NOT NULL DEFAULT '' COMMENT 'failed | ok',
  trigger_type  VARCHAR(32)  NOT NULL DEFAULT '',
  -- 🔴 三态，不是布尔：
  --    sent    发出去了
  --    skipped 规则判定不该发（自动+成功）—— 这是**正常**的，不是故障
  --    failed  该发但没发出去 —— 这才是要查的
  state         VARCHAR(16)  NOT NULL DEFAULT '',
  -- 为什么是这个 state。ShouldNotify 的 Reason 直接落这里，
  -- 好回答「这条为什么没发」而不用去翻代码
  reason        VARCHAR(255) NOT NULL DEFAULT '',
  err_msg       VARCHAR(500) NOT NULL DEFAULT '',
  attempts      INT          NOT NULL DEFAULT 0 COMMENT '投递尝试次数，含重试',
  content       TEXT         NULL COMMENT '实际发出去的文案，便于事后核对',
  created_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_time (created_at),
  KEY idx_state (state, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='通知投递记录（三态：sent/skipped/failed）';
