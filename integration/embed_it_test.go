package integration

import (
	"context"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// EmbAudit 是 Go 项目里复用公共字段的常规写法（对标 GORM 的 gorm:"embedded"）。
type EmbAudit struct {
	CreatedAt string `db:"created_at"`
	UpdatedAt string `db:"updated_at"`
}

// EmbUser 匿名嵌入 EmbAudit。
// 框架原先把它当成**一个普通列**：AutoMigrate 建出伪列、Insert 把整个结构体当参数绑定
// （驱动报 unsupported type）、Col 因匿名字段与外层同地址而返回伪列名
// （不报错，却生成指向错误列的查询条件）。现按扁平化展开。
type EmbUser struct {
	ID   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
	EmbAudit
}

func (EmbUser) TableName() string { return "emb_users" }

// TestEmbeddedFlattenOnRealDB —— 匿名嵌入必须被真正扁平化，而不是「不报错」就算完：
// AutoMigrate 建出的是 created_at / updated_at 两个真列，值写得进、读得回，
// Col 与按列的更新都按真实列名工作。
//
// 单元测试只能证明 SQL 形状，这里证明的是数据在真库里的最终去向。
func TestEmbeddedFlattenOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		// 用例入口先 DROP，保证可重复执行。
		if _, err := orm.RawExec(ctx, db, "DROP TABLE IF EXISTS emb_users"); err != nil {
			t.Fatalf("[%s] DROP 失败：%v", b.name, err)
		}
		if err := db.AutoMigrate(ctx, &EmbUser{}); err != nil {
			t.Fatalf("[%s] AutoMigrate 失败：%v", b.name, err)
		}

		u := EmbUser{Name: "alice", EmbAudit: EmbAudit{CreatedAt: "c1", UpdatedAt: "u1"}}
		// 嵌入字段若被整个当成一个参数绑定，这里会报 unsupported type。
		if err := orm.Insert(ctx, db, &u); err != nil {
			t.Fatalf("[%s] Insert 失败：%v", b.name, err)
		}
		if u.ID == 0 {
			t.Fatalf("[%s] 自增主键未回填", b.name)
		}

		got, err := orm.SelectById[EmbUser](ctx, db, u.ID)
		if err != nil {
			t.Fatalf("[%s] SelectById 失败：%v", b.name, err)
		}
		// 行映射必须按多级索引写回嵌入结构体里的字段。
		if got.CreatedAt != "c1" || got.UpdatedAt != "u1" {
			t.Fatalf("[%s] 嵌入字段读回错误：CreatedAt=%q UpdatedAt=%q", b.name, got.CreatedAt, got.UpdatedAt)
		}
		if got.Name != "alice" {
			t.Fatalf("[%s] 外层字段读回错误：%q", b.name, got.Name)
		}

		// Col 对 promoted 字段必须解析出真列名：修复前返回伪列名 emb_audit，
		// 真库侧报 unknown column。
		createdAt := orm.Col[EmbUser](func(e *EmbUser) *string { return &e.CreatedAt })
		n, err := orm.Count(ctx, db, orm.NewQuery[EmbUser]().Eq(createdAt, "c1"))
		if err != nil {
			t.Fatalf("[%s] 按嵌入字段查询失败：%v", b.name, err)
		}
		if n != 1 {
			t.Fatalf("[%s] 按 created_at 查询应命中 1 行，实际 %d", b.name, n)
		}

		// 按列的更新（map 路径）也要能定位到多级字段。
		if _, err := orm.UpdateByIdSets[EmbUser](ctx, db, u.ID, map[string]any{"updated_at": "u2"}); err != nil {
			t.Fatalf("[%s] UpdateByIdSets 改嵌入字段失败：%v", b.name, err)
		}
		got2, err := orm.SelectById[EmbUser](ctx, db, u.ID)
		if err != nil {
			t.Fatalf("[%s] 二次读回失败：%v", b.name, err)
		}
		if got2.UpdatedAt != "u2" {
			t.Fatalf("[%s] 嵌入字段更新未生效：%q", b.name, got2.UpdatedAt)
		}
	})
}
