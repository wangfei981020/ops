-- 023 拆掉「平台级采集范围」
--
-- 🔴 为什么加了又拆：它是**给一个错配打的补丁**。
--
--    项目的 workload_exclude 本来只该管"这个项目视图显示什么"，却挡住了采集，
--    于是被所有项目排掉、又不属于任何项目的服务，谁都看不见。
--    当时没去改配置，而是加了一条"绕过项目规则的独立采集链路"：
--    按 ns 采一份全量存成 project_id=0，专供「汇总」视图读。
--
--    真正的解法在 store/org.go 的注释里早就写着 ——
--    **「采集要全（数据留着以后能查），比对才收窄」**。
--    生产已经照这条改了：清空 workload_exclude、ns 列全、
--    不想对账的服务改用方案里的忽略规则（那个对所有列对称生效）。
--    根子解决了，补丁就没有存在理由。
--
--    而且「汇总」视图本身也拆了 —— 两个视图能看到的服务数不一样
--    （实测过 122 vs 168）而界面上毫无提示，谁也说不清自己看的是全量还是子集。
--    视图一拆，这份数据就再没有任何消费者，却仍在每轮打客户系统一次。
--
-- ⚠️ 先清 project_id=0 的快照，再删表 —— 顺序不能反。
--    留着那些行的话，「不限项目」查询（project_id > 0 那个条件）虽然挡得住，
--    但库里躺着一批谁也说不清来历的数据，迟早有人拿它去比。

DELETE FROM service_versions WHERE project_id = 0;
DELETE FROM service_pods    WHERE project_id = 0;
DELETE FROM version_changes WHERE project_id = 0;
DELETE FROM excluded_services WHERE project_id = 0;

DROP TABLE IF EXISTS org_env_scopes;
