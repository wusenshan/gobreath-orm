package integration

import (
	"context"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// 本文件验证 AutoMigrate 的「补齐缺失列」在真库上真的成立。
//
// 为什么必须真库：mock 只能证明「生成了 ALTER 语句」，证明不了
//   ① 该方言的 ALTER 语法真的合法；② 存量行的新增列读回来是零值而不是报错；
//   ③ 自省 SQL（information_schema / pragma_table_info）真的能查到列。
// 尤其第 ③ 条：SQLite 的 pragma 表值函数、MySQL 的 DATABASE() 都是实测才知道能不能用的。

// MigV1 / MigV2 指向同一张表，V2 比 V1 多 email（普通列）与 score（带索引）。
type MigV1 struct {
	ID   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
	Age  int    `db:"age"`
}

func (MigV1) TableName() string { return "it_mig_evolve" }

type MigV2 struct {
	ID    int64  `db:"id,pk,autoincrement"`
	Name  string `db:"name"`
	Age   int    `db:"age"`
	Email string `db:"email"`
	Score int    `db:"score,index"`
}

func (MigV2) TableName() string { return "it_mig_evolve" }

// TestAutoMigrateAddsMissingColumns 覆盖「模型加字段 → 已有表补列」这条真实演进路径。
//
// 修复前：表已存在时 AutoMigrate 只发 CREATE TABLE IF NOT EXISTS，
// 新列永远建不出来，之后所有针对新列的查询/写入才在运行期炸。
func TestAutoMigrateAddsMissingColumns(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		// 入口先清场：上一轮若在断言处中断会留下残表，
		// 让「补列后只有 1 行」这类计数断言成倍偏大（假阳性）。
		if _, err := orm.RawExec(ctx, db, "DROP TABLE IF EXISTS it_mig_evolve"); err != nil {
			t.Fatalf("[%s] 清场失败：%v", b.name, err)
		}
		if err := db.AutoMigrate(ctx, &MigV1{}); err != nil {
			t.Fatalf("[%s] AutoMigrate(V1) 失败：%v", b.name, err)
		}
		if err := orm.Insert(ctx, db, &MigV1{Name: "alice", Age: 18}); err != nil {
			t.Fatalf("[%s] 插入 V1 数据失败：%v", b.name, err)
		}
		// 反向锚点：补列前**写入新列**必须失败（has no column named / does not exist）。
		//
		// 锚点只能用写，不能用读，读在两个层面上都是静默的：
		//   · `SELECT *` —— 结果集里没有的列，行映射把字段留零值，不报错（既有设计）；
		//   · SQLite 的 `WHERE "email" = ?` —— 双引号标识符解析不成列时会被
		//     SQLite 兜底成**字符串字面量**（它的知名 misfeature），
		//     于是条件变成 'email' = 'bob@x.com' → 恒 false → 0 行且不报错。
		emailCol := orm.Col[MigV2](func(m *MigV2) *string { return &m.Email })
		if err := orm.Insert(ctx, db, &MigV2{Name: "bob", Age: 20, Email: "bob@x.com"}); err == nil {
			t.Fatalf("[%s] 补列前写入新列竟能成功，断言失去意义", b.name)
		}

		if err := db.AutoMigrate(ctx, &MigV2{}); err != nil {
			t.Fatalf("[%s] AutoMigrate(V2) 补列失败：%v", b.name, err)
		}

		rows, err := orm.SelectList(ctx, db, orm.NewQuery[MigV2]())
		if err != nil {
			t.Fatalf("[%s] 补列后按 V2 查询仍失败（列没真的加上）：%v", b.name, err)
		}
		if len(rows) != 1 {
			t.Fatalf("[%s] 存量行应仍在且只有 1 行，实际 %d", b.name, len(rows))
		}
		if rows[0].Name != "alice" || rows[0].Age != 18 {
			t.Fatalf("[%s] 存量行数据被破坏：%+v", b.name, rows[0])
		}
		if rows[0].Email != "" || rows[0].Score != 0 {
			t.Fatalf("[%s] 新增列存量行应读回零值，实际 email=%q score=%d", b.name, rows[0].Email, rows[0].Score)
		}

		// 新列必须能写能读：只「加得上」而写不进等于没修
		if err := orm.Insert(ctx, db, &MigV2{Name: "bob", Age: 20, Email: "bob@x.com", Score: 7}); err != nil {
			t.Fatalf("[%s] 写入新列失败：%v", b.name, err)
		}
		got, err := orm.SelectOne(ctx, db, orm.NewQuery[MigV2]().Eq(emailCol, "bob@x.com"))
		if err != nil {
			t.Fatalf("[%s] 按新列查询失败：%v", b.name, err)
		}
		if got.Score != 7 {
			t.Fatalf("[%s] 新列读回值不对：score=%d", b.name, got.Score)
		}

		// 幂等：再迁一次既不报错，也不会把已存在的列再加一遍
		if err := db.AutoMigrate(ctx, &MigV2{}); err != nil {
			t.Fatalf("[%s] 重复 AutoMigrate(V2) 报错：%v", b.name, err)
		}
		n, err := orm.Count(ctx, db, orm.NewQuery[MigV2]())
		if err != nil || n != 2 {
			t.Fatalf("[%s] 重复迁移后行数应为 2，实际 %d（err=%v）", b.name, n, err)
		}
	})
}

// ---- 补齐的 unique 列必须真的带上唯一约束 ----

type MigUqV1 struct {
	ID   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
}

func (MigUqV1) TableName() string { return "it_mig_unique" }

type MigUqV2 struct {
	ID   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
	Code string `db:"code,unique"`
}

func (MigUqV2) TableName() string { return "it_mig_unique" }

// TestAutoMigrateAddsUniqueIndexOnMissingColumn：SQLite 的 ADD COLUMN 不接受 UNIQUE 修饰，
// 所以补列时改走 CREATE UNIQUE INDEX —— 这里验证唯一性**真的生效**，而不是只建了个普通列。
func TestAutoMigrateAddsUniqueIndexOnMissingColumn(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		if _, err := orm.RawExec(ctx, db, "DROP TABLE IF EXISTS it_mig_unique"); err != nil {
			t.Fatalf("[%s] 清场失败：%v", b.name, err)
		}
		if err := db.AutoMigrate(ctx, &MigUqV1{}); err != nil {
			t.Fatalf("[%s] AutoMigrate(V1) 失败：%v", b.name, err)
		}
		if err := db.AutoMigrate(ctx, &MigUqV2{}); err != nil {
			t.Fatalf("[%s] AutoMigrate(V2) 补唯一列失败：%v", b.name, err)
		}
		if err := orm.Insert(ctx, db, &MigUqV2{Name: "a", Code: "same"}); err != nil {
			t.Fatalf("[%s] 写入唯一列失败：%v", b.name, err)
		}
		if err := orm.Insert(ctx, db, &MigUqV2{Name: "b", Code: "same"}); err == nil {
			t.Fatalf("[%s] 补齐的 unique 列未生效：重复 code 竟然插入成功", b.name)
		}
	})
}
