-- 019 去掉「基准列」——判定改成横着比这几列彼此，没有参照物这回事
--
-- 🔴 为什么是删列而不是留着不读：
--
--    留着的话，保存过的方案里那份 baseline_json 还在库里躺着，
--    哪天有人（或某个还没改完的代码路径）把它读回去，判定就会
--    悄悄回到"相对某一列"的旧语义 —— 而这种分叉不报任何错，
--    表现只是"同一个方案，两个人看到的结论不一样"。
--
--    判定语义变了就该让旧数据消失，而不是让它有机会复活。
--
-- ⚠️ 用户会看到的变化：保存过的方案再打开时，"基准"那一项没有了。
--    方案本身（选了哪几列、忽略了什么、只看差异）**全部保留**。

-- baseline_pin（手工版本基线）一并删掉。
-- 它和 baseline_json 不是一回事（它是手填一个 tag、所有列跟它比），
-- 但同样是"跟某个参照物比"的思路，这次一起清。
ALTER TABLE comparison_plans DROP COLUMN baseline_pin;

-- ⚠️ baseline_json 是 NOT NULL 且无默认值，直接 DROP 即可 ——
--    它只被 ListPlans/SavePlan 读写，那两处已经不再引用。
ALTER TABLE comparison_plans DROP COLUMN baseline_json;
