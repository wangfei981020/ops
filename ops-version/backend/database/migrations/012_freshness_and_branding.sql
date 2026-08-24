-- 数据新鲜度 + 品牌自定义。

-- ─────────── 每列（平台×环境）的采集状态 ───────────
--
-- 🔴 为什么不能沿用 orgs.last_sync_*：那是**平台级**的，
-- 而"列"是平台×环境。一个平台的 UAT 采成功、PROD 采失败时，
-- 平台级字段只能记其中一个，另一个的失败就此消失。
--
-- ⚠️ 与 service_versions.observed_at 分工不同：
--   observed_at        = 数据**本身**是什么时候的（采集失败时保持不动，正是我们要的）
--   last_collect_at    = 上一次**尝试**采集是什么时候
-- 两者一起才能表达"半小时前试过，失败了，所以你看到的还是两小时前的数据"。
-- 只有前者会把"一直没采成功"显示成"数据很旧"，看不出是**采集坏了**还是**对方没发版**。
SET @s := IF(EXISTS(SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='org_envs' AND COLUMN_NAME='last_collect_at'),
  'DO 0',
  'ALTER TABLE org_envs
     ADD COLUMN last_collect_at     DATETIME     NULL COMMENT ''上次尝试采集的时刻'',
     ADD COLUMN last_collect_status VARCHAR(32)  NOT NULL DEFAULT '''' COMMENT ''success|auth_failed|forbidden|unreachable|error'',
     ADD COLUMN last_collect_error  VARCHAR(500) NOT NULL DEFAULT ''''');
PREPARE st FROM @s;
EXECUTE st;
DEALLOCATE PREPARE st;

-- ─────────── 品牌自定义（白标）───────────
--
-- 单条。图片直接存库（base64 data URI）而不是存文件：
-- ⚠️ 后端是无状态多副本的，存本地文件会出现"A 副本能看到、B 副本 404"，
-- 而那种问题只在扩容后才暴露。存对象存储又要多一个依赖，
-- 而 logo 只有几十 KB，进库最省事。
CREATE TABLE IF NOT EXISTS branding (
  id           TINYINT UNSIGNED NOT NULL DEFAULT 1,
  app_name     VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '为空则用产品内置名',
  -- 侧栏与登录页的 logo。为空则用 @ops/ui 内置的那个图标
  logo_data    MEDIUMTEXT   NULL COMMENT 'data URI，如 data:image/svg+xml;base64,...',
  -- 浏览器标签图标。与 logo 分开：favicon 要方形小尺寸，
  -- 直接拿宽幅 logo 当 favicon 会糊成一团
  favicon_data MEDIUMTEXT   NULL COMMENT 'data URI',
  -- 登录页左栏那句定位。为空则用产品默认文案
  tagline      VARCHAR(255) NOT NULL DEFAULT '',
  updated_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  updated_by   VARCHAR(64)  NOT NULL DEFAULT '',
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='品牌自定义（白标）';

INSERT IGNORE INTO branding (id) VALUES (1);
