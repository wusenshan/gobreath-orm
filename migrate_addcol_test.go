package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
)

// 本文件覆盖 AutoMigrate 的「补齐缺失列」路径。
//
// 原行为（v0.1.12 及以前）：表已存在时只发 CREATE TABLE IF NOT EXISTS，
// 模型后来新增的列**永远不会被建出来** —— 之后所有 SELECT / INSERT 才在
// 运行期报 no such column，而 AutoMigrate 本身一声不响地成功返回。

// ---- 测试模型：同一张表的两个版本（v2 比 v1 多 email / score / code 三列） ----

type MigEvolveV1 struct {
	Id   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
	Age  int    `db:"age"`
}

func (MigEvolveV1) TableName() string { return "mig_evolve" }

type MigEvolveV2 struct {
	Id    int64  `db:"id,pk,autoincrement"`
	Name  string `db:"name"`
	Age   int    `db:"age"`
	Email string `db:"email"`
	Score int    `db:"score,index"`
	Code  string `db:"code,unique"`
}

func (MigEvolveV2) TableName() string { return "mig_evolve" }

// migUnknownDialect 模拟「自实现方言」：既不是内置三方言，也没实现 DialectKinder。
// 框架无从知道它的系统表长什么样 —— 此时必须走「仅建表」，不能拿 SQLite 的 PRAGMA 去猜。
type migUnknownDialect struct{ Dialect }

func TestTableColumnsSQLPerDialect(t *testing.T) {
	// PG：未指定 schema 时用当前模式（写死 'public' 会漏掉自定义 search_path）
	q, ok := tableColumnsSQL(PG, "", "users")
	if !ok {
		t.Fatal("PG 应支持自省")
	}
	if !strings.Contains(q, "current_schema()") || !strings.Contains(q, "'users'") {
		t.Fatalf("PG 未指定模式时应查 current_schema()，实际: %s", q)
	}
	q, _ = tableColumnsSQL(PG, "app", "users")
	if !strings.Contains(q, "'app'") {
		t.Fatalf("PG 指定模式时应按该模式过滤，实际: %s", q)
	}

	// MySQL：未指定库时用当前库
	q, ok = tableColumnsSQL(MySQL, "", "users")
	if !ok {
		t.Fatal("MySQL 应支持自省")
	}
	if !strings.Contains(q, "DATABASE()") || !strings.Contains(q, "'users'") {
		t.Fatalf("MySQL 未指定库时应查 DATABASE()，实际: %s", q)
	}

	// SQLite：pragma 表值函数（而不是 PRAGMA table_info —— 那会返回 6 列，无法共用一个 Scan）
	if q, _ = tableColumnsSQL(SQLite, "", "users"); q != "SELECT name FROM pragma_table_info('users')" {
		t.Fatalf("SQLite 自省 SQL 不符: %s", q)
	}
	q, _ = tableColumnsSQL(SQLite, "main", "users")
	if !strings.Contains(q, "'main'") {
		t.Fatalf("SQLite 指定附加库时应带上库名参数，实际: %s", q)
	}

	// 未知方言：明确不支持，调用方据此退化为「仅建表」
	if _, ok := tableColumnsSQL(migUnknownDialect{}, "", "users"); ok {
		t.Fatal("未知方言不应声称支持自省")
	}
	if got := dialectKind(migUnknownDialect{}); got != "sqlite" {
		t.Fatalf("未知方言的兜底类别仍应是 sqlite（列类型映射用），实际 %q", got)
	}
}

func TestMissingFieldsNormalizesCase(t *testing.T) {
	meta := getMeta[MigEvolveV2]()
	// 数据库返回的列名大小写取决于方言（PG 小写、MySQL/SQLite 原样），比较必须归一
	existing := map[string]struct{}{"ID": {}, "name": {}, "Age": {}}
	miss := missingFields(meta, existing)
	got := make([]string, 0, len(miss))
	for _, f := range miss {
		got = append(got, f.colName)
	}
	want := "email,score,code"
	if strings.Join(got, ",") != want {
		t.Fatalf("缺失列应为 %s，实际 %v", want, got)
	}
	if n := len(missingFields(meta, map[string]struct{}{
		"id": {}, "name": {}, "age": {}, "email": {}, "score": {}, "code": {},
	})); n != 0 {
		t.Fatalf("列齐全时不应有缺失列，实际 %d", n)
	}
}

func TestAddColumnStatementsSQLite(t *testing.T) {
	meta := getMeta[MigEvolveV2]()
	miss := missingFields(meta, map[string]struct{}{"id": {}, "name": {}, "age": {}})
	stmts, err := addColumnStatements(meta, SQLite, "", miss)
	if err != nil {
		t.Fatalf("补齐列不应报错: %v", err)
	}
	joined := strings.Join(stmts, "\n")
	for _, want := range []string{
		`ALTER TABLE "mig_evolve" ADD COLUMN "email" TEXT`,
		`ALTER TABLE "mig_evolve" ADD COLUMN "score" INTEGER`,
		// SQLite 的 ADD COLUMN 不接受 UNIQUE 修饰，故改为建唯一索引
		`CREATE UNIQUE INDEX IF NOT EXISTS "uidx_mig_evolve_code" ON "mig_evolve" ("code")`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，实际:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "ADD COLUMN") && strings.Contains(joined, "UNIQUE\" ") {
		t.Fatalf("不应生成带 UNIQUE 修饰的 ADD COLUMN（SQLite 不支持）:\n%s", joined)
	}
}

// TestAddColumnRejectsMissingPrimaryKey 挡住「在已有表上补主键」：
// 存量行需要一个凭空造出来的唯一值，SQLite 更是直接不支持，框架不能替调用方决定。
func TestAddColumnRejectsMissingPrimaryKey(t *testing.T) {
	meta := getMeta[MigEvolveV2]()
	_, err := addColumnStatements(meta, SQLite, "", missingFields(meta, map[string]struct{}{}))
	if err == nil {
		t.Fatal("缺主键/自增列时应报错而不是补列")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Fatalf("报错应点名缺失的列，实际: %v", err)
	}
}

// TestAutoMigrateAddsMissingColumnsViaMock 走完整 AutoMigrate：
// mock 返回「表里已有 id/name/age」，于是应补出 email / score / code，再建索引。
func TestAutoMigrateAddsMissingColumnsViaMock(t *testing.T) {
	db := newMockDB(t)
	hook := &testHook{}
	db = db.WithHooks(hook)
	// 注册已有列；defer 还原，避免影响其他用例（mockRegistry 是包级 map）
	mockRegistry["pragma_table_info"] = &mockRows{
		cols: []string{"name"},
		data: [][]driver.Value{{"id"}, {"name"}, {"age"}},
	}
	defer delete(mockRegistry, "pragma_table_info")

	if err := db.AutoMigrate(context.Background(), &MigEvolveV2{}); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}
	sqls := hookQueries(hook)
	joined := strings.Join(sqls, "\n")
	for _, want := range []string{
		`CREATE TABLE IF NOT EXISTS "mig_evolve"`,
		`ALTER TABLE "mig_evolve" ADD COLUMN "email" TEXT`,
		`ALTER TABLE "mig_evolve" ADD COLUMN "score" INTEGER`,
		`ALTER TABLE "mig_evolve" ADD COLUMN "code" TEXT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "uidx_mig_evolve_code"`,
		`CREATE INDEX IF NOT EXISTS "idx_mig_evolve_score"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，实际执行:\n%s", want, joined)
		}
	}
	// 顺序：索引必须建在补列之后，否则「列不存在」会直接失败
	iAlter := indexOfContains(sqls, "ALTER TABLE")
	iIdx := indexOfContains(sqls, "CREATE INDEX")
	if iAlter < 0 || iIdx < 0 || iAlter > iIdx {
		t.Fatalf("ALTER ADD COLUMN 必须早于 CREATE INDEX，实际:\n%s", joined)
	}
}

// TestAutoMigrateNoAlterWhenTableAbsent：表不存在（自省返回空）时，
// CREATE TABLE 已把列建全，不应再多发 ALTER。
func TestAutoMigrateNoAlterWhenTableAbsent(t *testing.T) {
	db := newMockDB(t)
	hook := &testHook{}
	db = db.WithHooks(hook)
	// 不注册任何结果集 → 自省得到空集合
	if err := db.AutoMigrate(context.Background(), &MigEvolveV2{}); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}
	for _, s := range hookQueries(hook) {
		if strings.Contains(s, "ALTER TABLE") {
			t.Fatalf("表不存在时不应补列，实际执行:\n%s", strings.Join(hookQueries(hook), "\n"))
		}
	}
}

// TestAutoMigrateUnknownDialectStaysCreateOnly：未知方言拿不到系统表信息，
// 保持 v0.1.12 的「仅建表」行为 —— 既不报错，也不发 SQLite 的 PRAGMA 去瞎猜。
func TestAutoMigrateUnknownDialectStaysCreateOnly(t *testing.T) {
	// 包装内置方言但没有 DialectKinder —— 正是「框架认不出它是哪一类」的场景：
	// 方法转发到 SQLite 可用（建表能跑），但自省必须被判为不支持。
	sqlDB, err := sql.Open("ormmock", "")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db := NewDB(sqlDB, migUnknownDialect{SQLite})
	hook := &testHook{}
	db = db.WithHooks(hook)
	if err := db.AutoMigrate(context.Background(), &MigEvolveV2{}); err != nil {
		t.Fatalf("未知方言的 AutoMigrate 不应因自省失败而报错: %v", err)
	}
	for _, s := range hookQueries(hook) {
		if strings.Contains(s, "ALTER TABLE") || strings.Contains(s, "pragma_table_info") {
			t.Fatalf("未知方言不应补列或查系统表，实际执行:\n%s", strings.Join(hookQueries(hook), "\n"))
		}
	}
}

// hookQueries 取出 hook 里记录的 SQL（只取 before 阶段，避免一条语句记两遍）。
func hookQueries(h *testHook) []string {
	out := make([]string, 0, len(h.events))
	for _, e := range h.events {
		if e.Phase == HookPhaseBefore {
			out = append(out, e.Query)
		}
	}
	return out
}

func indexOfContains(list []string, sub string) int {
	for i, s := range list {
		if strings.Contains(s, sub) {
			return i
		}
	}
	return -1
}
