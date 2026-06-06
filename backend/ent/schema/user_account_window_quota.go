package schema

import (
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
)

// UserAccountWindowQuota 记录 用户 × 上游账号 × 官方窗口（5h/7d）维度的"按官方利用率百分比"配额台账。
//
// 与 UserPlatformQuota（按 USD 绝对额度 + 日/周/月日历窗口）不同：
//   - 标尺是上游官方上报的窗口利用率百分比（如 Codex 的 codex_5h_used_percent / codex_7d_used_percent）；
//   - 每次请求把账号级利用率增量分摊到发起请求的用户，累计到 limit_percent 即拦截；
//   - 窗口重置跟随官方刷新（window_reset_at），到点或观测到回落即清零。
type UserAccountWindowQuota struct {
	ent.Schema
}

func (UserAccountWindowQuota) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_account_window_quotas"},
	}
}

func (UserAccountWindowQuota) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (UserAccountWindowQuota) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.Int64("account_id"),

		// window_type: 官方窗口类型，'5h'（5 小时滚动窗口）或 '7d'（7 天/周窗口）。
		field.String("window_type").
			MaxLen(8).
			NotEmpty().
			Validate(func(s string) error {
				switch s {
				case "5h", "7d":
					return nil
				default:
					return fmt.Errorf("window_type %q is not allowed", s)
				}
			}),

		// limit_percent: 该用户在此账号此窗口可占用的官方利用率百分点上限（默认 23）。
		field.Float("limit_percent").
			SchemaType(map[string]string{dialect.Postgres: "decimal(7,4)"}).
			Default(23),

		// attributed_percent: 当前窗口内已分摊到该用户的官方利用率百分点。
		field.Float("attributed_percent").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(0),

		// window_reset_at: 当前窗口的官方重置时间（镜像账号 codex_5h_reset_at / codex_7d_reset_at），
		// 到点由后台清零兜底；NULL = 尚未观测到官方窗口信息。
		field.Time("window_reset_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UserAccountWindowQuota) Indexes() []ent.Index {
	return []ent.Index{
		// 软删除友好唯一索引：同用户同账号同窗口只允许一条活跃记录。
		index.Fields("user_id", "account_id", "window_type").
			Unique().
			Annotations(entsql.IndexWhere("deleted_at IS NULL")),
		// 重置扫描热路径：按账号 + 窗口批量清零。
		index.Fields("account_id", "window_type"),
		index.Fields("user_id"),
	}
}
