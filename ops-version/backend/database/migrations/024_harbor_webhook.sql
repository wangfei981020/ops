-- 024 Harbor webhook 接入：把「推的是哪个版本」这个信息拿回来
--
-- 🔴 为什么必须走 webhook：
--
--    对账页的「镜像同步」判定用的是 (服务名 + 版本号) 去查复制记录。
--    而我们原来只轮询 Harbor 的 REST API：
--      GET /api/v2.0/replication/executions/{id}/tasks
--    这个接口的 src/dst_resource 在多 artifact 时写的是
--      `appA/bi-central-backend [1 item(s) in total]`
--    —— **不带 tag**。实测过 143 条复制记录，tag 100% 为空，
--    于是归因永远匹配不上，全部落到「未同步 / 状态未知」。
--
--    而 Harbor **webhook** 的 REPLICATION 事件里有：
--      event_data.replication.successful_artifact[].name_tag
--        = "bi-central-backend:20260824083840-15f85908-223"
--    这正是缺的那一半。同一件事，两个数据源，只有一个带版本号。
--
-- ⚠️ webhook 不能替代轮询，两者各补一半：
--    webhook 有版本号但会丢事件（Harbor 重启、我们自己发版的那几十秒、网络抖动，
--    而且 Harbor 不重发）；轮询拿不到版本号但能事后补齐 execution 级的成败。
--    所以两条链路并存，写同一张 sync_tasks。

-- webhook 认证密钥。Harbor 那边填进「Auth Header」，我们做常量时间比较。
--
-- 🔴 这个端点能写入对账依据 —— 没有认证等于让任何人伪造「已同步」，
--    而伪造出来的绿灯没有任何地方能看出异常。
-- ⚠️ 与 harbors 表分开：webhook 是**我们这边的接收配置**，
--    而 harbors 存的是「怎么去连对方」，两者的生命周期和权限都不同。
CREATE TABLE IF NOT EXISTS webhook_tokens (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name        VARCHAR(64)  NOT NULL COMMENT '给人看的用途说明，如「Harbor 复制事件」',
  token_hash  VARBINARY(64) NOT NULL COMMENT '只存哈希，明文仅在创建时显示一次',
  enabled     TINYINT(1)   NOT NULL DEFAULT 1,
  last_used_at DATETIME    NULL COMMENT '最近一次被成功调用 —— 用来判断「配了但从没生效」',
  created_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_by  VARCHAR(64)  NOT NULL DEFAULT '',
  deleted_at  DATETIME     NULL,
  PRIMARY KEY (id),
  KEY idx_enabled (enabled, deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT='入站 webhook 的认证令牌（目前只有 Harbor 复制事件用）';

-- sync_tasks 加来源标记。
--
-- 🔴 界面上必须分得出「这条是实时推送来的」还是「轮询补的」：
--    轮询那份没有版本号，拿它下「未同步」的结论是无据的。
--    不标来源的话，两份数据混在一起，谁也说不清某一条的可信度。
SET @c := (SELECT COUNT(*) FROM information_schema.COLUMNS
            WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sync_tasks' AND COLUMN_NAME = 'source');
SET @s := IF(@c = 0,
  'ALTER TABLE sync_tasks ADD COLUMN source VARCHAR(16) NOT NULL DEFAULT ''poll'' COMMENT ''poll=轮询REST API（无版本号）｜webhook=Harbor事件推送（有版本号）''',
  'DO 0');
-- ⚠️ 这三条必须**各占一行**。执行器按「行尾分号」切语句（runner.go 的 splitSQL），
--    写在同一行会整段送给 MySQL → Error 1064。
-- 🔴 而且这个错**用 mysql CLI 验不出来**（CLI 按分号切，与行数无关）——
--    CLI 全绿、执行器直接崩。迁移只能拿执行器验，别拿 CLI 验。
--    017 的注释里就写过这条，这次还是踩了。
PREPARE stmt FROM @s;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
