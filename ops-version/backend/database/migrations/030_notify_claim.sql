-- 030 一次复制只发一张卡：用一张占位表把"谁来发"定死
--
-- # 为什么需要它
--
-- 🔴 **Harbor 是每推一个镜像发一次 webhook**，不是一次复制发一次。
--    生产实测（2026-09-17，用户截图）：同一个 execution 22204 连着三张卡，
--    每张只有 1 个镜像，而卡片上的「成功」数是 14 → 15 → 16 一路递增。
--
--    于是一次手动同步 100 个镜像 = **100 张卡**。飞书自定义机器人的限频是
--    100 次/分钟、**5 次/秒** —— 秒级连发必然撞上，后面的卡片直接发不出去。
--    同时我们每收一个事件还要回查一次 execution + 一次 tasks，
--    对 Harbor 的请求也跟着翻 100 倍。
--
-- 改成"整次复制结束后汇总发一张"之后，新问题是**并发**：
-- execution 转入终态的那一刻，排队中的剩余事件会**同时**看到"已终态"，
-- 于是同时去发 —— 查一下 notify_records 有没有发过（AlreadyNotified）挡不住，
-- 那是"先查后写"，两个请求都能查到"没发过"。
--
-- 所以要一个**原子占位**：主键冲突天然互斥，谁插进去谁负责发。
--
-- ⚠️ 发送失败时占位要删掉，否则采集器那条兜底路径永远补不了这次执行 ——
--    而"webhook 发失败"恰恰是兜底存在的全部理由。

CREATE TABLE IF NOT EXISTS notify_claims (
  policy_ref  BIGINT UNSIGNED NOT NULL COMMENT 'sync_policies.id',
  exec_id     BIGINT       NOT NULL COMMENT 'Harbor 的 execution id',
  claimed_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  claimed_by  VARCHAR(32)  NOT NULL DEFAULT '' COMMENT 'webhook | collector，排查时看是谁发的',
  -- 🔴 主键就是去重键。INSERT 冲突 = 别人已经在发了，我不发。
  PRIMARY KEY (policy_ref, exec_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='通知占位：一次复制执行只允许发一张卡';
