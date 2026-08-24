-- OpsVersion 003 · MCP 接入令牌
--
-- 🔴 一个接入方一条令牌，且**绑角色**。
--    别的系统上踩过：一条全局 token 谁拿到都是全权限，
--    出了事既查不出是谁调的，也没法只吊销其中一方。
CREATE TABLE IF NOT EXISTS mcp_tokens (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name          VARCHAR(64)  NOT NULL COMMENT '接入方名字，如 运维AI助手',
  -- 只存哈希。明文只在创建时返回一次，之后任何接口都拿不到 ——
  -- 能被读出来的令牌等于没有令牌
  token_hash    VARCHAR(191) NOT NULL,
  token_prefix  VARCHAR(16)  NOT NULL DEFAULT '' COMMENT '前 8 位，仅用于界面辨识',

  role_code     VARCHAR(32)  NOT NULL DEFAULT 'viewer' COMMENT '与用户共用同一套角色',
  -- 数据范围，同 users.visible_instances。空 = 不限
  visible_instances VARCHAR(500) NOT NULL DEFAULT '',

  enabled       TINYINT(1)   NOT NULL DEFAULT 1,
  last_used_at  DATETIME     NULL,
  expires_at    DATETIME     NULL COMMENT 'NULL = 永不过期',
  created_by    VARCHAR(64)  NOT NULL DEFAULT '',
  created_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deleted_at    DATETIME     NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_hash (token_hash),
  KEY idx_enabled (enabled, deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='MCP 接入令牌';
