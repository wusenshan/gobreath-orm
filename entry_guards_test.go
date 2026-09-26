package orm

import (
	"context"
	"database/sql/driver"
	"testing"
)

// 本文件为「构造期闸门」类修复补回归用例 —— 这些修复此前只有代码级核对、没有 gate
// （详见《缺陷修复与真库验证报告》第一章的复核订正）：
//   P0-3 匿名嵌入结构体解析期硬失败
//   P1-4 Nearest 的 k <= 0 拒绝
//   P1-6 读写路由先剥注释 + WITH 保守判写
//   P2-5 结果集重名列直接报错
//
// 这些闸门都是「panic / 报错」型契约，用例必须断言「真的拦下」，而不是只跑通正常路径；
// 同时反向断言合法写法不受影响，防止修过头。

// --- 匿名嵌入结构体的扁平化 ---

// GuardEmbBase 是 Go 里复用公共字段的常规写法（对标 GORM 的 gorm:"embedded"）。
// 框架原先把它当成**一个普通列**（建伪列 / 绑定整个结构体 / Col 返回错误列名），
// 后来改成解析期 panic（升级即崩），最终改为真正的扁平化展开。
type GuardEmbBase struct {
	CreatedAt string `db:"created_at"`
	UpdatedAt string `db:"updated_at"`
}

type guardEmbBase struct { // 未导出嵌入：被 f.PkgPath != "" 过滤，本就不会被当列
	CreatedAt string
}

type guardEmbModel struct {
	Id   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
	GuardEmbBase
}

func (guardEmbModel) TableName() string { return "guard_emb" }

type guardEmbModelUnexported struct {
	Id   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
	guardEmbBase
}

type guardEmbModelIgnored struct {
	Id           int64  `db:"id,pk,autoincrement"`
	Name         string `db:"name"`
	GuardEmbBase `db:"-"`
}

// 指针嵌入：nil 指针的读写语义未定义，暂不支持，必须明确报错。
type guardEmbPtr struct {
	Id   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
	*GuardEmbBase
}

// 展开后与外层字段撞列名：必须报错，不能静默二选一。
type guardEmbConflict struct {
	Id           int64  `db:"id,pk,autoincrement"`
	GuardEmbBase        // 含 created_at
	CreatedAt    string `db:"created_at"`
}

func TestEmbeddedStructIsFlattened(t *testing.T) {
	m := getMeta[guardEmbModel]()
	got := map[string]bool{}
	for _, f := range m.fields {
		got[f.colName] = true
	}
	// 嵌入的字段与外层字段一起成为列，而不是一个叫 base 的伪列。
	for _, col := range []string{"id", "name", "created_at", "updated_at"} {
		if !got[col] {
			t.Fatalf("嵌入字段应被扁平化展开，缺少列 %q（实际 %v）", col, colNames(m))
		}
	}
	if got["base"] || got["guard_emb_base"] {
		t.Fatalf("嵌入字段不应再生成伪列：%v", colNames(m))
	}
	// 取值路径必须是多级索引（嵌入字段下标 + 内层下标），否则读写会定位错字段。
	found := false
	for _, f := range m.fields {
		if f.colName == "created_at" {
			found = true
			if len(f.idx) != 2 {
				t.Fatalf("嵌入字段的 idx 应为多级，实际 %v", f.idx)
			}
		}
	}
	if !found {
		t.Fatal("未找到 created_at 列")
	}
}

func TestEmbeddedColResolvesPromotedField(t *testing.T) {
	// 修复前这里返回伪列名 "base"：嵌入字段与外层结构体同地址，
	// 一层反查命中偏移 0 的嵌入字段（不报错，却生成指向错误列的查询条件）。
	if got := Col[guardEmbModel](func(m *guardEmbModel) *string { return &m.CreatedAt }).name; got != "created_at" {
		t.Fatalf("promoted 字段的列解析错误：got %q，期望 created_at", got)
	}
	// 外层字段不受影响。
	if got := Col[guardEmbModel](func(m *guardEmbModel) *string { return &m.Name }).name; got != "name" {
		t.Fatalf("外层字段解析错误：got %q，期望 name", got)
	}
}

func TestEmbeddedRowScanWritesPromotedField(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)
	key := `FROM "guard_emb"`
	mockRegistry[key] = &mockRows{
		cols: []string{"id", "name", "created_at", "updated_at"},
		data: [][]driver.Value{{int64(7), "n", "c", "u"}},
	}
	defer delete(mockRegistry, key)
	rows, err := SelectList(ctx, db, NewQuery[guardEmbModel]())
	if err != nil {
		t.Fatalf("SelectList: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("期望 1 行，实际 %d", len(rows))
	}
	// 行映射必须写回嵌入结构体里的字段（按多级 idx 定位）。
	if rows[0].CreatedAt != "c" || rows[0].UpdatedAt != "u" {
		t.Fatalf("嵌入字段未写回：CreatedAt=%q UpdatedAt=%q", rows[0].CreatedAt, rows[0].UpdatedAt)
	}
	if rows[0].Name != "n" || rows[0].Id != 7 {
		t.Fatalf("外层字段写回错误：%+v", rows[0])
	}
}

func TestEmbeddedSkipAndErrors(t *testing.T) {
	// db:"-" 显式跳过整组嵌入字段（被忽略的字段仍以 ignore=true 留在 fields 里）。
	if n := mappedCols(getMeta[guardEmbModelIgnored]()); n != 2 {
		t.Fatalf("db:\"-\" 的嵌入字段应整组跳过，实际映射到 %d 列", n)
	}
	// 未导出嵌入：既不该 panic 也不该产生列。
	if n := mappedCols(getMeta[guardEmbModelUnexported]()); n != 2 {
		t.Fatalf("未导出嵌入不应产生列，实际映射到 %d 列", n)
	}
	// 指针嵌入：明确报错，不能静默当列。
	expectPanic(t, "指针嵌入", func() { _ = getMeta[guardEmbPtr]() })
	// 展开后撞列名：报错，不能静默二选一。
	expectPanic(t, "嵌入展开后列名冲突", func() { _ = getMeta[guardEmbConflict]() })
}

// colNames 返回元数据里所有列的列名（ignore 字段不计）。
func colNames(m *modelMeta) []string {
	var out []string
	for _, f := range m.fields {
		if !f.ignore {
			out = append(out, f.colName)
		}
	}
	return out
}

// mappedCols 返回元数据里真正映射成列的字段数（ignore=true 的不计）。
func mappedCols(m *modelMeta) int {
	n := 0
	for _, f := range m.fields {
		if !f.ignore {
			n++
		}
	}
	return n
}

// --- P1-4 Nearest 的 k ---

func TestGuardRejectsNonPositiveNearestK(t *testing.T) {
	emb := Col[Article](func(a *Article) *[]float32 { return &a.Embedding })
	for _, k := range []int{0, -1} {
		expectPanic(t, "Nearest k<=0", func() {
			NewQuery[Article]().Nearest(emb, []float32{0.1, 0.2}, k)
		})
	}
	// 合法 k 不受影响，且 LIMIT 仍在。
	q := NewQuery[Article]().WithDialect(PG).Nearest(emb, []float32{0.1, 0.2}, 5)
	s, _ := q.Build()
	if !containsSub(s, "LIMIT 5") {
		t.Fatalf("k=5 应生成 LIMIT 5: %s", s)
	}
}

// --- P1-6 读写路由判定 ---

func TestGuardIsWriteQuerySeesPastLeadingComments(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"/* trace-id: 7 */ UPDATE t SET a = 1", true},
		{"-- note\nDELETE FROM t", true},
		{"#note\nINSERT INTO t VALUES (1)", true},
		{"\n\t  /* a */ /* b */ DROP TABLE t", true},
		{"/* 未闭合 UPDATE t", false}, // 注释未闭合→返回空串，交数据库报错，不猜语义
		{"WITH x AS (SELECT 1) DELETE FROM t", true},
		{"WITH x AS (SELECT 1) SELECT * FROM x", false}, // 纯只读 CTE：词法扫描未发现写关键字，可以走副本
		{"SELECT * FROM t", false},
		{"SELECT * FROM t FOR UPDATE", true},
		{"", false},
	}
	for _, c := range cases {
		if got := isWriteQuery(c.sql); got != c.want {
			t.Fatalf("isWriteQuery(%q) = %v，期望 %v", c.sql, got, c.want)
		}
	}
}

// TestGuardCTEReadGoesToReplica 覆盖 CTE 路由的精确判定：只读 CTE 走副本，
// 任何位置含写关键字（含 PG data-modifying CTE 里写在 CTE 内部的情况）都走主库，
// 而「看起来像写关键字」的标识符 / 字符串 / 注释 / 引号标识符不得误判。
func TestGuardCTEReadGoesToReplica(t *testing.T) {
	cases := []struct {
		sql  string
		want bool // true = 判写（走主库）
		why  string
	}{
		{"WITH x AS (SELECT * FROM t) SELECT * FROM x", false, "纯只读 CTE"},
		{"WITH a AS (SELECT 1), b AS (SELECT 2) SELECT * FROM a, b", false, "多个只读 CTE"},
		{"WITH x AS (SELECT insert_count FROM t) SELECT * FROM x", false, "insert_count 是标识符，不是 INSERT"},
		{"WITH x AS (SELECT * FROM t WHERE name = 'UPDATE') SELECT * FROM x", false, "字符串字面量里的 UPDATE"},
		{"WITH x AS (SELECT * FROM t WHERE name = 'it''s INSERT') SELECT * FROM x", false, "'' 转义后的字符串"},
		{"WITH x AS (SELECT /* INSERT INTO t */ 1) SELECT * FROM x", false, "块注释里的 INSERT"},
		{"WITH x AS (SELECT 1 -- DELETE FROM t\n) SELECT * FROM x", false, "行注释里的 DELETE"},
		{`WITH x AS (SELECT * FROM "INSERT_LOG") SELECT * FROM x`, false, "PG 引号标识符"},
		{"WITH x AS (SELECT * FROM `insert`) SELECT * FROM x", false, "MySQL 反引号标识符"},
		{"WITH x AS (SELECT 1) UPDATE t SET a = 1", true, "主句是 UPDATE"},
		{"WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x", true, "主句是 INSERT"},
		{"WITH m AS (DELETE FROM t RETURNING *) INSERT INTO t SELECT * FROM m", true, "PG data-modifying CTE：写在 CTE 内部"},
		{"WITH x AS (SELECT * FROM t) SELECT * FROM x FOR UPDATE", true, "CTE + 悲观锁读必须走主库"},
		{"WITH x AS (SELECT 'unterminated FROM t) SELECT * FROM x", true, "字符串未闭合 → 不可靠 → 保守判写"},
	}
	for _, c := range cases {
		if got := isWriteQuery(c.sql); got != c.want {
			t.Fatalf("isWriteQuery(%q) = %v，期望 %v（%s）", c.sql, got, c.want, c.why)
		}
	}
}

// --- P2-5 结果集重名列 ---

type guardDupRow struct {
	Id   int64  `db:"id,pk"`
	Name string `db:"name"`
}

func (guardDupRow) TableName() string { return "guard_dup" }

func TestGuardRejectsDuplicateRowColumns(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)
	key := `FROM "guard_dup"`

	// 两列都映射到字段（JOIN 后 SELECT *，两表都有 name）→ 必须报错，不能「最后一个静默胜出」。
	mockRegistry[key] = &mockRows{
		cols: []string{"id", "name", "name"},
		data: [][]driver.Value{{int64(1), "a", "b"}},
	}
	if _, err := SelectList(ctx, db, NewQuery[guardDupRow]()); err == nil {
		t.Fatal("结果集出现映射到字段的重名列时应报错")
	}
	// 原生 SQL 走同一条扫描路径（RawQuery 的 DTO 场景同样要拦）。
	if _, err := RawQuery[guardDupRow](ctx, db, `SELECT id, name, name FROM "guard_dup"`); err == nil {
		t.Fatal("RawQuery 遇到同名列时也应报错")
	}
	// 重复但不映射到任何字段的列（如自定义投影里重复出现的计算列）应保持宽松。
	mockRegistry[key] = &mockRows{
		cols: []string{"id", "name", "dist", "dist"},
		data: [][]driver.Value{{int64(1), "a", 0.1, 0.2}},
	}
	rows, err := SelectList(ctx, db, NewQuery[guardDupRow]())
	if err != nil || len(rows) != 1 {
		t.Fatalf("未映射字段的重复列不应报错：rows=%d err=%v", len(rows), err)
	}
	delete(mockRegistry, key)
}

// --- 辅助 ---

func expectPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("%s 期望 panic，实际正常返回", what)
		}
	}()
	fn()
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
