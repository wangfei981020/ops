-- 028 清掉「同一次推送里重复的、没有版本号的」历史记录
--
-- # 这些行是怎么来的
--
--    sync_tasks 有两条写入路径，v0.72.2 之前过滤标准不一致：
--
--      webhook（实时）   趁复制刚结束回查 API，拿得到版本号，且丢弃空版本号
--      采集器（30 分钟）  遍历**所有历史 execution** 拉明细，全盘落库
--
--    而 Harbor 的 resource 字段只在复制刚结束时有值。日志实测：采集器
--    查询时刻距 task 结束的中位数是 258 天 —— 拿回的必然是 resource=null、
--    没有版本号的陈年 task。
--
--    唯一键是 (policy_ref, exec_id, service_key, tag)，tag 也在里面，
--    而两条路径的 exec_id 还不一样（webhook 填 0，采集器填真实执行号）——
--    于是空版本号那条和带版本号那条**互不冲突、并排存着**。
--    界面上同一个服务、同一个时刻出现两行，一行有版本号，
--    一行写着「Harbor 未记录版本」，看的人无从判断哪个是真的。
--
--    v0.72.2 已经堵住了新增（SaveTasks 不再落库空版本号），
--    这一条清理的是**在那之前积下的存量**。
--
-- # 判据：只删「同一时刻已经有带版本号记录」的那条
--
-- 🔴 用 finished_at 配对，**不能用 exec_id** —— 两条路径写的 exec_id 不同
--    （webhook 恒为 0），拿它配对一条都匹配不上。
--
-- ⚠️ 没有配对的空记录**一律保留**。它们是那次同步唯一的痕迹，
--    删掉的话「这次到底推没推过」就再也答不上来了 ——
--    而「答不上版本号」和「查不到这次同步」是两件事，后者更糟。
--    （生产上这类有 161 条，主要来自没有配 webhook 的复制规则。
--    要让它们也有版本号，得在 Harbor 上给对应项目补配 webhook，
--    那是配置的事，不是数据能补的。）
--
-- ⚠️ finished_at 为 NULL 的行不会被删：SQL 里 NULL = NULL 不成立。
--    这正是我们要的 —— 时刻都对不上，就谈不上「同一次推送」。
--
-- 幂等：DELETE 再跑一次匹配不到任何行。

DELETE t FROM sync_tasks t
JOIN sync_tasks x
  ON x.policy_ref  = t.policy_ref
 AND x.service_key = t.service_key
 AND x.finished_at = t.finished_at
 AND x.tag <> ''
WHERE t.tag = '';
