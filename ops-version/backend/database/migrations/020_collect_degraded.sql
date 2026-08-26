-- 020 记下「这一列是不是降级采集的」
--
-- 🔴 为什么必须落库：降级发生在采集那一刻，而看表的人是几小时后打开对账页的。
--    只写日志的话，这条信息永远到不了他面前。
--
--    降级的具体后果是：读不到 deployments 时改从 Pod 反推服务版本，
--    于是**副本为 0 的服务在 Pod 层没有任何 Pod → 采不到 → 对账时落成
--    「该组织未部署此服务」**，而事实是"我们看不见它"。
--    副本缩到 0 是常规运维动作，生产上 ls-uat 命名空间此刻就有 5 个 scaled-0 的服务。
--
-- ⚠️ 这是「该组织未部署此服务」这句话的第四种成因。前三种见 
--    对方真没部署（对）/ 我方规则排除（错）/ 采集失败（走 no_data，对）。
--    四种里三种是错的，而它们在界面上长得一模一样。

-- 只加在环境级：降级是**按集群**发生的，同一个平台的 UAT 可能降级、PROD 不降级。
-- 加在平台级会让两个环境共用一个标记，其中一个的真相就此消失
-- （与 last_collect_status 当初分环境记的理由完全相同）。
ALTER TABLE org_envs
  ADD COLUMN last_collect_degraded TINYINT(1) NOT NULL DEFAULT 0
    COMMENT '上次采集是否走了降级路径（读不到 deployments，从 Pod 反推）',
  ADD COLUMN last_collect_degraded_note VARCHAR(255) NOT NULL DEFAULT ''
    COMMENT '降级原因，给人看的一句话，直接显示在对账表头的悬停提示里';
