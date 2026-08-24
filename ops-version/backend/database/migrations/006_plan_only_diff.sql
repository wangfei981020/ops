-- OpsVersion 006 · comparison_plans 补 only_diff 列
--
-- 🔴 这个字段此前是「声明了但从不落库」：
--    store.ComparePlan 有 OnlyDiff、API 收得下、前端也传得上来，
--    只有表里没这一列 —— 于是保存时静默丢弃，读出来恒为 false。
--    表现是「保存方案时勾了『只看差异』，下次套用永远没勾」，
--    不报错、不掉数据，只是那个开关看起来失灵。
--
-- 这类缺陷靠读代码很难发现（三处都写了这个字段，只有 SQL 没写），
-- 是拿界面自己调的接口与界面显示逐字段比才撞出来的。
ALTER TABLE comparison_plans
  ADD COLUMN only_diff TINYINT(1) NOT NULL DEFAULT 0 AFTER baseline_pin;
