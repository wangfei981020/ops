-- 031 通知人：同步失败时在 Teams 群里 @ 谁
--
-- 通知人单独维护（名字 + Teams 登录账号），每个 Teams 渠道勾选要 @ 的人，可多选。
-- 只用于 Teams 渠道、只在同步失败时 @；飞书渠道不受影响。
--
-- ⚠️ 账号必须手填，不按名字推导：Teams 的工作流只回 202、不告诉你账号存不存在，
--    推导出一个不存在的账号时，同一张卡片里其他人的名字会跟着错位
--    （日志告警那边 10-21 实际出过一次）。手填至少让「填的是谁」有人负责。
--
-- 硬删除，不做软删：通知人只被 notify_channel_mentions 引用，删人时一起删掉关联即可；
-- 软删的唯一键还得绕生成列（见 018），这里没必要。

CREATE TABLE IF NOT EXISTS notify_contacts (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name           VARCHAR(64)  NOT NULL COMMENT '卡片上 @ 显示的名字',
  teams_account  VARCHAR(254) NOT NULL COMMENT 'Teams 登录账号（一般是公司邮箱），存小写',
  note           VARCHAR(255) NOT NULL DEFAULT '',
  enabled        TINYINT(1)   NOT NULL DEFAULT 1 COMMENT '停用 = 所有渠道都不再 @ 他，关联保留',
  created_at     DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_contact_account (teams_account)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='通知人：Teams 渠道同步失败时 @ 的人';

CREATE TABLE IF NOT EXISTS notify_channel_mentions (
  channel_id  BIGINT UNSIGNED NOT NULL COMMENT 'notify_channels.id',
  contact_id  BIGINT UNSIGNED NOT NULL COMMENT 'notify_contacts.id',
  PRIMARY KEY (channel_id, contact_id),
  KEY idx_mention_contact (contact_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='渠道 → 同步失败时 @ 的通知人';
