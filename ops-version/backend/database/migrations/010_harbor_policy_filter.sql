-- OpsVersion 010 · 指定只拉哪几条复制规则
--
-- 🔴 为什么需要：一个 Harbor 上可能有几十条 replication policy，
-- 而跟版本比对有关的只有几条。全量拉的代价不是"多几行界面"，是**请求量**：
--   每条规则要拉 20 次执行（Executions），
--   每次已完成的执行还要拉一次 tasks，失败的 task 还要再拉一次 log
-- 几十条规则 × 20 次执行 = 上千次请求，每轮采集都打一遍 —— 会把 Harbor 拖慢。
--
-- ⚠️ 与「绑组织」不是一回事：
--   绑组织   = 这条规则推给谁（归因用）
--   本字段   = 我们压根只关心这几条（采集用）
ALTER TABLE harbors
  ADD COLUMN policy_filter TEXT NULL
    COMMENT '换行分隔，支持 * 通配。留空=拉全部规则' AFTER insecure_tls;
