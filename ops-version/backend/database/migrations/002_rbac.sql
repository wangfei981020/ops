-- OpsVersion 002 · 用户 / 角色 / 审计
--
-- 🔴 RBAC 在阶段 1 就建，不留到最后。
-- CMDB 是 235 条路由做完才补的接口级门控，补的过程要逐条回头改，代价远大于一开始就带上。

CREATE TABLE IF NOT EXISTS users (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  username      VARCHAR(64)  NOT NULL,
  display_name  VARCHAR(128) NOT NULL DEFAULT '',
  email         VARCHAR(191) NOT NULL DEFAULT '',
  -- bcrypt。auth_source=sso 时为空
  password_hash VARCHAR(191) NOT NULL DEFAULT '',
  auth_source   VARCHAR(32)  NOT NULL DEFAULT 'local' COMMENT 'local | sso',

  role_code     VARCHAR(32)  NOT NULL DEFAULT 'viewer'
                COMMENT 'super_admin | admin | editor | viewer',

  -- 🔴 数据范围，独立于角色的另一个维度。
  --    N 个项目并存时，A公司的对接人不该看到 B公司的版本 —— 光靠角色控制不住这个。
  --    空 = 不限；否则是逗号分隔的 instance_id
  visible_instances VARCHAR(500) NOT NULL DEFAULT '',

  enabled       TINYINT(1)   NOT NULL DEFAULT 1,
  last_login_at DATETIME     NULL,
  created_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at    DATETIME     NULL,
  PRIMARY KEY (id),
  -- 唯一索引带 deleted_at：软删后同名可重建（不带的话删掉的账号永久占用用户名）
  UNIQUE KEY uk_username (username, deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='用户';

-- 会话。
-- 🔴 单独存表而不是纯 JWT，是因为**改角色必须能立刻踢掉会话**：
--    角色在 token 里，不重登看着像没生效 —— 降权时尤其危险，旧权限还留着。
CREATE TABLE IF NOT EXISTS sessions (
  id          VARCHAR(64)  NOT NULL,
  user_id     BIGINT UNSIGNED NOT NULL,
  expires_at  DATETIME     NOT NULL,
  created_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_user (user_id),
  KEY idx_exp (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='会话（改角色时按 user_id 清）';

-- 审计。
-- 🔴 「谁在什么时候往对方推了什么镜像」是这个系统最需要能查的一件事。
--    所有写操作都要落这里，含建账号 / 改角色 / 手动触发同步。
CREATE TABLE IF NOT EXISTS audit_logs (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  actor       VARCHAR(64)  NOT NULL,
  action      VARCHAR(64)  NOT NULL COMMENT 'instance.create | sync.trigger | user.role_change …',
  target      VARCHAR(191) NOT NULL DEFAULT '',
  detail      TEXT         NULL     COMMENT 'JSON。🔴 写入前必须脱敏，绝不能记凭据明文',
  -- 🔴 必须记结果：只记「发起了操作」而不记成败，
  --    会让一次 401 的操作在审计里看起来跟成功的一模一样（CMDB 上栽过：202 被记成 success）
  result      VARCHAR(16)  NOT NULL DEFAULT 'success' COMMENT 'success | failed',
  error_msg   VARCHAR(500) NOT NULL DEFAULT '',
  ip          VARCHAR(64)  NOT NULL DEFAULT '',
  created_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_actor_time (actor, created_at),
  KEY idx_action_time (action, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='审计日志';
