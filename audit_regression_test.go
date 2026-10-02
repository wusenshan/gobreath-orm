package orm

import (
	"context"
	"strings"
	"testing"
)

// 本文件固定一轮静态审查里确认的四个缺陷。它们都有共同特征：
// 单元测试与真库测试都没覆盖到那条**组合路径**，所以能长期存活。
// 每条都写明「旧行为是什么、为什么会错」，避免日后被当成冗余断言删掉。

// ---------------------------------------------------------------- 乐观锁：map 更新

// verUser 带 ,version 列，用于 map 形式的乐观锁更新。
type verUser struct {
	ID      int64  `db:"id,pk"`
	Name    string `db:"name"`
	Version int    `db:"version,version"`
}

func (verUser) TableName() string { return "ver_users" }

// UpdateByIdSets 在 sets 未带版本列时既不该自增版本、也不该判冲突。
//
// 旧行为：setParts 里判的是 `vi != nil`（而不是 hasVersion），于是
// `UpdateByIdSets(id, {"name":"x"})` 会生成 `SET "name" = $1, "version" = "version" + 1`
// 且**没有** `WHERE version = ?` —— 每次 map 更新都静默抬版本，其它会话拿着
// 刚读到的版本再写就会收到假的 ErrOptimisticLock（并没有并发写者）。
func TestUpdateByIdSetsWithoutVersionDoesNotBumpVersion(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)

	if _, err := UpdateByIdSets[verUser](ctx, db, int64(7), map[string]any{"name": "x"}); err != nil {
		t.Fatalf("UpdateByIdSets 失败：%v", err)
	}
	if strings.Contains(recQuery, `"version"`) {
		t.Fatalf("sets 未带版本列时不应触碰版本列，实际 SQL：%s", recQuery)
	}
	if !strings.Contains(recQuery, `SET "name" = $1`) {
		t.Fatalf("SQL 形状不符：%s", recQuery)
	}
	if len(recArgs) != 2 || recArgs[0] != "x" || recArgs[1] != int64(7) {
		t.Fatalf("参数应为 [x 7]，实际 %v", recArgs)
	}

	// 带上版本列时，乐观锁语义必须照旧：SET 自增 + WHERE 旧值。
	if _, err := UpdateByIdSets[verUser](ctx, db, int64(7),
		map[string]any{"name": "y", "version": 3}); err != nil {
		t.Fatalf("带版本列的 UpdateByIdSets 失败：%v", err)
	}
	if !strings.Contains(recQuery, `"version" = "version" + 1`) {
		t.Fatalf("带版本列时应自增版本，实际 SQL：%s", recQuery)
	}
	if !strings.Contains(recQuery, `AND "version" = $3`) {
		t.Fatalf("带版本列时应追加 WHERE version = 旧值，实际 SQL：%s", recQuery)
	}
	if len(recArgs) != 3 {
		t.Fatalf("参数个数应为 3，实际 %d：%v", len(recArgs), recArgs)
	}
	// 注意：database/sql 会把 int 参数规整成 int64 交给驱动，
	// 所以这里只能与 int64 比较（`recArgs[2] != 3` 会因 int64 vs int 恒真）。
	if recArgs[2] != int64(3) {
		t.Fatalf("参数应为 [y 7 3]，实际 %v（%T）", recArgs, recArgs[2])
	}
}

// ---------------------------------------------------------------- 向量阈值 + 折叠条件

// 条件被折叠成空（NotIn(空) 按集合语义恒真、整组省略）时，向量阈值过滤
// 不能再按 `len(q.groups) > 0` 判断「已有 WHERE」——那会拼出
// `SELECT ... FROM "t" AND "vec" <-> $1 < $2` 这种非法 SQL。
func TestWithinDistanceWithFoldedEmptyCondition(t *testing.T) {
	q := NewQuery[Article]().WithDialect(PG).
		NotIn(Col[Article](func(a *Article) *string { return &a.Title }), []any{}).
		WithinDistance(Col[Article](func(a *Article) *[]float32 { return &a.Embedding }), []float32{1, 2, 3}, 0.5)
	sqlStr, args := q.Build()

	if strings.Contains(sqlStr, "AND") && !strings.Contains(sqlStr, "WHERE") {
		t.Fatalf("拼出了没有 WHERE 的 AND 子句：%s", sqlStr)
	}
	if !strings.Contains(sqlStr, "WHERE") {
		t.Fatalf("向量阈值过滤必须带 WHERE：%s", sqlStr)
	}
	if strings.Contains(sqlStr, "NOT IN") {
		t.Fatalf("NotIn(空) 应整组省略：%s", sqlStr)
	}
	// 参数顺序：向量恒为 $1，阈值为 $2（没有条件参数混进来）。
	if len(args) != 2 {
		t.Fatalf("应只绑定向量与阈值两个参数，实际 %v", args)
	}
}

// ---------------------------------------------------------------- Preload：父类型无主键

// preloadNoPK 刻意既无 ID 字段也无 ,pk tag。
type preloadNoPK struct {
	Dept  string    `db:"dept"`
	Posts []verUser `db:"-" orm:"has_many;fk:user_id"`
}

func (preloadNoPK) TableName() string { return "no_pk_rows" }

// has_many / has_one 靠父主键关联子表外键；父类型没有主键时旧实现在
// collectPK 里直接 `meta.pk.colName` nil 解引用 panic（子类型有防线、父类型没有）。
// 这里要求得到带关系名与模型名的 error。
func TestPreloadHasManyWithoutParentPKReturnsError(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)
	rows := []preloadNoPK{{Dept: "d1"}}
	err := Preload(ctx, db, &rows, "Posts")
	if err == nil {
		t.Fatalf("父类型无主键时应返回 error，实际 nil（旧实现会 panic）")
	}
	if !strings.Contains(err.Error(), "主键") {
		t.Fatalf("错误信息应点明主键问题，实际：%v", err)
	}
}

// ---------------------------------------------------------------- 自定义方言的分类

// customDialect 模拟「包装内置方言」的第三方实现：只嵌 Dialect，
// 不实现 DialectKinder，因此只能靠内置判定兜底。
type customDialect struct{ Dialect }

// kindedDialect 实现 DialectKinder，声明自己按 PG 处理。
type kindedDialect struct {
	Dialect
	kind string
}

func (d kindedDialect) DialectKind() string { return d.kind }

// 「只有 Offset 没有 LIMIT」的补全必须按方言种类走，而不是按具体类型断言：
// 自定义方言以前一律落到 default 分支补 `LIMIT -1`，PG 系会报
// "LIMIT must not be negative"、MySQL 系报 1064。
func TestOffsetOnlyPagingAcrossDialects(t *testing.T) {
	cases := []struct {
		name string
		d    Dialect
		want string // 期望出现的分页片段
	}{
		{"内置 PG", PG, "OFFSET 20"},
		{"PG 指针", &postgresDialect{}, "OFFSET 20"},
		{"内置 MySQL", MySQL, "LIMIT 18446744073709551615"},
		{"MySQL 指针", &mysqlDialect{}, "LIMIT 18446744073709551615"},
		{"内置 SQLite", SQLite, "LIMIT -1"},
		{"自报 PG 的自定义方言", kindedDialect{Dialect: PG, kind: "postgres"}, "OFFSET 20"},
		{"自报 MySQL 的自定义方言", kindedDialect{Dialect: MySQL, kind: "mysql"}, "LIMIT 18446744073709551615"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sqlStr, _ := NewQuery[verUser]().WithDialect(c.d).Offset(20).Build()
			if !strings.Contains(sqlStr, "OFFSET 20") || !strings.Contains(sqlStr, c.want) {
				t.Fatalf("期望包含 %q 与 OFFSET 20，实际：%s", c.want, sqlStr)
			}
			// PG 系绝不能出现负数 LIMIT。
			if dialectKind(c.d) == "postgres" && strings.Contains(sqlStr, "LIMIT -1") {
				t.Fatalf("PG 系方言不应补 LIMIT -1：%s", sqlStr)
			}
		})
	}
}
