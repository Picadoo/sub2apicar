-- 用户 × 上游账号 × 官方窗口（5h/7d）维度的"按官方利用率百分比"配额台账。
-- 与 user_platform_quotas（按 USD 绝对额度 + 日/周/月日历窗口）不同：
--   - 标尺是上游官方上报的窗口利用率百分比（codex_5h_used_percent / codex_7d_used_percent）；
--   - 每次请求把账号级利用率增量分摊到发起请求的用户，累计到 limit_percent 即拦截；
--   - 窗口重置跟随官方刷新（window_reset_at），到点或观测到回落即清零。
-- 软删除：deleted_at IS NULL 的记录为活跃记录，部分唯一索引保证同用户同账号同窗口只有一条活跃配额。

CREATE TABLE IF NOT EXISTS user_account_window_quotas (
    id                  BIGSERIAL PRIMARY KEY,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_id          BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,

    -- 官方窗口类型：'5h'（5 小时滚动窗口）或 '7d'（7 天/周窗口）
    window_type         VARCHAR(8) NOT NULL CHECK (window_type IN ('5h', '7d')),

    -- 该用户在此账号此窗口可占用的官方利用率百分点上限（默认 23）
    limit_percent       DECIMAL(7,4) NOT NULL DEFAULT 23,

    -- 当前窗口内已分摊到该用户的官方利用率百分点
    attributed_percent  DECIMAL(10,4) NOT NULL DEFAULT 0,

    -- 当前窗口的官方重置时间（镜像账号 codex_5h_reset_at / codex_7d_reset_at），
    -- 到点由后台清零兜底；NULL = 尚未观测到官方窗口信息。
    window_reset_at     TIMESTAMPTZ,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);

-- 软删除友好唯一索引：同用户同账号同窗口只允许一条未删除记录
CREATE UNIQUE INDEX IF NOT EXISTS useraccountwindowquota_user_account_window_uq
    ON user_account_window_quotas (user_id, account_id, window_type)
    WHERE deleted_at IS NULL;

-- 重置扫描热路径：按账号 + 窗口批量清零
CREATE INDEX IF NOT EXISTS useraccountwindowquota_account_id_window_type
    ON user_account_window_quotas (account_id, window_type);

CREATE INDEX IF NOT EXISTS useraccountwindowquota_user_id
    ON user_account_window_quotas (user_id);
