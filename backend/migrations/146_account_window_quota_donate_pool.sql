-- 5h 窗口「救急池」捐赠比例。
-- 用户可把自己 5h 窗口份额的一部分捐入救急池：donate_pool_fraction ∈ [0,1] = 捐出占自己 5h 上限的比例。
--   0   = 不捐（默认，行为同以前的硬性 23%）
--   0.5 = 捐出自己 5h 份额的一半，另一半永远自留（即时可用）
--   1   = 把未用部分全捐
-- 仅对 5h 行有意义；7d 行恒为 0（周配额是铁底线，不可捐、不可借）。
-- 该字段由后端裸 SQL 读写（与本表 attributed/limit 的重算逻辑一致，未纳入 ent 生成代码）；
-- 跨窗口重置保留（出差期间持续捐赠），用户回来手动调回 0 即可。

ALTER TABLE user_account_window_quotas
    ADD COLUMN IF NOT EXISTS donate_pool_fraction DECIMAL(5,4) NOT NULL DEFAULT 0;
