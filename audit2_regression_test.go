package orm

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// 本文件是第二轮走查的回归用例，逐条对应一批「不报错但做错事」的缺陷：
//
//  1. Nearest 的 k 被 Count / Page 当成分页 LIMIT 清掉
//  2. UpdateById 自增版本后不回写实体（同一实体连续两次更新必报假的乐观锁冲突）
//  3. 批量 Update 把实体版本原样写回，而不是各行自增
//  4. assignInt / assignUint / assignFloat 忽略 strconv 错误，字段静默恒 0
//  5. Json / Having 的 op 无白名单（注释声称支持若干运算符，实际原样拼接）
//  6. StrictTagCheck 下并发写 m.tagChecked 是数据竞态
//  7. BatchInsert / BatchUpsert 不分片，几万占位符撞方言参数上限
//  8. Limit / Offset 负数静默失效
//
// 每条用例在修复前都必须失败（mutation check 见提交说明）。

// ---- 1. Nearest 的 k 不能被 Count / Page 吞掉 ----

func vecEmbCol() ColExpr {
	return Col[Article](func(a *Article) *[]float32 { return &a.Embedding })
}

// TestCountKeepsNearestK 近邻查询的 total 必须是「邻域内有多少条」，而不是全表行数。
//
// 修之前 k 存在 limit 里，被 countBuild 与 limit 一起清掉：Count 退化成全表计数。
// 真库用例只插了 3 行、k 恰好也是 3，两者相等，所以 mock 之外的用例测不出来 ——
// 这里直接断言 SQL 里仍然带着 k。
func TestCountKeepsNearestK(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)
	q := NewQuery[Article]().Nearest(vecEmbCol(), []float32{0.1, 0.2, 0.3}, 3)
	if _, err := Count(ctx, db, q); err != nil {
		t.Fatalf("Count 失败: %v", err)
	}
	if !strings.Contains(recQuery, "LIMIT 3") {
		t.Fatalf("近邻查询的 Count 必须保留 k（LIMIT 3），否则 total 会变成全表行数: %s", recQuery)
	}
	// LIMIT 对单行的 COUNT(*) 不起作用，必须包一层子查询才截断得了。
	if !strings.Contains(recQuery, "COUNT(*) FROM (") {
		t.Fatalf("带 k 的计数必须走子查询（LIMIT 放在单行 COUNT(*) 上无效）: %s", recQuery)
	}
	if strings.Contains(recQuery, "AS dist") {
		t.Fatalf("计数查询不该追加距离列: %s", recQuery)
	}
}

// TestCountFastPathStaysFlat 反向断言：没有 distinct / groupBy / having / k 时
// 仍走 COUNT(*) 快速路径（防止为修上面那条把计数一律包成子查询）。
func TestCountFastPathStaysFlat(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)
	q := NewQuery[Article]().Eq(Col[Article](func(a *Article) *string { return &a.Title }), "go")
	if _, err := Count(ctx, db, q); err != nil {
		t.Fatalf("Count 失败: %v", err)
	}
	if strings.Contains(recQuery, "COUNT(*) FROM (") {
		t.Fatalf("普通计数应保持快速路径，不包子查询: %s", recQuery)
	}
	if strings.Contains(recQuery, "LIMIT") {
		t.Fatalf("普通计数不该带 LIMIT: %s", recQuery)
	}
}

// TestPageKeepsNearestK 分页的 size 不能把 k 冲掉：k 是召回上限，size 是页大小，
// 两者并存时取更严格的那个。
func TestPageKeepsNearestK(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		k    int
		size int
		want string
	}{
		{"k 小于页大小时听 k 的", 3, 10, "LIMIT 3"},
		{"页大小小于 k 时听页大小的", 20, 5, "LIMIT 5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := mockDB(t, PG)
			q := NewQuery[Article]().Nearest(vecEmbCol(), []float32{0.1, 0.2, 0.3}, c.k)
			if _, err := Page(ctx, db, q, 1, c.size); err != nil {
				t.Fatalf("Page 失败: %v", err)
			}
			// Page 先 Count 再 SelectList，recQuery 留下的是列表那条。
			if !strings.Contains(recQuery, c.want) {
				t.Fatalf("期望 %s，实际: %s", c.want, recQuery)
			}
		})
	}
}

// ---- 2 / 3. 乐观锁：版本回写与批量自增 ----

// TestUpdateByIdWritesBackBumpedVersion 库里的版本已经 +1，实体也必须跟着 +1，
// 否则同一个实体连续两次 UpdateById，第二次拿旧版本去比对 —— 收到一个
// 根本没有并发写者的 ErrOptimisticLock（假冲突）。
func TestUpdateByIdWritesBackBumpedVersion(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)
	p := &Post{Id: 1, Title: "t", Content: "c", Version: 5}
	if err := UpdateById(ctx, db, p); err != nil {
		t.Fatalf("UpdateById 失败: %v", err)
	}
	if p.Version != 6 {
		t.Fatalf("自增后的版本号应回写到实体（期望 6），实际 %d", p.Version)
	}
	// 第二次更新：WHERE 里带的必须是新版本 6，而不是实体最初那个 5。
	recArgs = nil
	if err := UpdateById(ctx, db, p); err != nil {
		t.Fatalf("第二次 UpdateById 失败: %v", err)
	}
	if len(recArgs) == 0 {
		t.Fatal("未记录到参数")
	}
	last := recArgs[len(recArgs)-1]
	if last != int64(6) {
		t.Fatalf("第二次更新的 WHERE version 应为 6（回写生效），实际 %v（%T）", last, last)
	}
	if p.Version != 7 {
		t.Fatalf("连续两次更新后版本应为 7，实际 %d", p.Version)
	}
}

// TestUpdateBumpsVersionInsteadOfWritingEntityValue 按条件批量更新时，
// 版本必须按各行当前值自增，而不是把实体里的版本号灌给所有匹配行。
func TestUpdateBumpsVersionInsteadOfWritingEntityValue(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)
	q := NewQuery[Post]().Eq(Col[Post](func(p *Post) *string { return &p.Title }), "x")
	if err := Update(ctx, db, q, &Post{Title: "new", Content: "c", Version: 5}); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if !strings.Contains(recQuery, `"version" = "version" + 1`) {
		t.Fatalf("批量 Update 应让版本自增，实际: %s", recQuery)
	}
	if strings.Contains(recQuery, `"version" = ?`) {
		t.Fatalf("批量 Update 不该把实体的版本号原样写回所有匹配行: %s", recQuery)
	}
}

// strVer 版本列是字符串：MySQL 上 `version + 1` 会把 "abc" 静默转成 0 再 +1
// （PG 直接报错），冲突检测形同虚设，必须显式失败。
type strVer struct {
	Id      int64  `db:"id,pk,autoincrement"`
	Name    string `db:"name"`
	Version string `db:"version,version"`
}

func TestUpdateByIdRejectsNonIntegerVersion(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)
	e := &strVer{Id: 1, Name: "n", Version: "abc"}
	err := UpdateById(ctx, db, e)
	if err == nil {
		t.Fatal("非整型版本列应报错（自增语义不成立），实际静默通过")
	}
	if !strings.Contains(err.Error(), "整型") {
		t.Fatalf("错误信息应指出版本列必须是整型，实际: %v", err)
	}
}

// ---- 4. 数值解析失败不能静默置 0 ----

func TestAssignNumericTextRejectsUnparsableValue(t *testing.T) {
	var i int
	iv := reflect.ValueOf(&i).Elem()
	// 小数列（NUMERIC(10,2) / DECIMAL）映射到整型字段：以前静默置 0。
	if err := assignInt(iv, "9.50"); err == nil {
		t.Fatal("无法解析成整数的文本必须报错，不能静默置 0")
	} else if !strings.Contains(err.Error(), "9.50") {
		t.Fatalf("错误信息应带上原始值: %v", err)
	}
	if i != 0 {
		t.Fatalf("解析失败时不应写入任何值，实际 %d", i)
	}
	// 整数值的数值文本（lib/pq 把 numeric 交回 []byte 时很常见）应照常接受。
	if err := assignInt(iv, "9.00"); err != nil {
		t.Fatalf("\"9.00\" 是整数值，应能赋值: %v", err)
	}
	if i != 9 {
		t.Fatalf("期望 9，实际 %d", i)
	}
	if err := assignInt(reflect.ValueOf(&i).Elem(), []byte("7")); err != nil || i != 7 {
		t.Fatalf("[]byte(\"7\") 应赋值 7，实际 %d / err=%v", i, err)
	}

	var u uint
	uv := reflect.ValueOf(&u).Elem()
	if err := assignUint(uv, "-1"); err == nil {
		t.Fatal("负数赋给无符号字段应报错")
	}
	if err := assignUint(uv, "3"); err != nil || u != 3 {
		t.Fatalf("\"3\" 应赋给 uint，实际 %d / err=%v", u, err)
	}

	var f float64
	fv := reflect.ValueOf(&f).Elem()
	if err := assignFloat(fv, "abc"); err == nil {
		t.Fatal("无法解析的浮点文本必须报错")
	}
	if err := assignFloat(fv, "1.5"); err != nil || f != 1.5 {
		t.Fatalf("\"1.5\" 应赋给 float64，实际 %v / err=%v", f, err)
	}
}

// ---- 5. Json / Having 的 op 白名单 ----

func TestJsonAndHavingOpWhitelist(t *testing.T) {
	nameCol := Col[User](func(u *User) *string { return &u.Name })
	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("%s：白名单外的 op 应 panic", name)
			}
		}()
		fn()
	}
	mustPanic("Json 的注入型 op", func() {
		NewQuery[User]().Json(nameCol, "a.b", "= 1 OR 1=1 --", 1)
	})
	mustPanic("Having 的 IN（HAVING 左侧是聚合表达式，只接受二元比较）", func() {
		NewQuery[User]().Having(nameCol, "IN", 1)
	})
	mustPanic("Having 的空 op", func() {
		NewQuery[User]().Having(nameCol, "", 1)
	})
	mustPanic("HavingCount 的注入型 op", func() {
		NewQuery[User]().HavingCount("> 0) OR (1=1 --", 1)
	})

	// 白名单内的写法照常工作，且大小写/空白被归一化。
	sqlStr, _ := NewQuery[User]().Having(nameCol, "  like  ", "x").WithDialect(PG).Build()
	if !strings.Contains(sqlStr, "HAVING") || !strings.Contains(sqlStr, "LIKE") {
		t.Fatalf("小写 like 应归一化成 LIKE: %s", sqlStr)
	}
	sqlStr, _ = NewQuery[User]().Json(nameCol, "a.b", "!=", "x").WithDialect(PG).Build()
	if !strings.Contains(sqlStr, `->>'b' != $1`) {
		t.Fatalf("Json 的 != 应照常渲染: %s", sqlStr)
	}
	sqlStr, _ = NewQuery[User]().Json(nameCol, "a.b", "in", "x").WithDialect(PG).Build()
	if !strings.Contains(sqlStr, " IN ($1)") {
		t.Fatalf("Json 接受单值 IN: %s", sqlStr)
	}
}

// ---- 6. StrictTagCheck 下的并发取模型 ----

// TestStrictTagCheckConcurrentGetMeta 同一个 *modelMeta 会被多个 goroutine 同时取到，
// 而 tagChecked 此前是普通 bool —— 并发读写是数据竞态（本仓库 -race 不可用，
// 这条用例至少保证「并发路径不崩、拿到的是同一份元数据」）。
func TestStrictTagCheckConcurrentGetMeta(t *testing.T) {
	strictTagCheck.Store(false)
	defer strictTagCheck.Store(false)
	typ := newTaggedType(`db:"id,pk,autoincrement"`, `db:"name"`)
	_ = getMetaByType(typ) // 先以非严格模式缓存，tagChecked = false

	strictTagCheck.Store(true)
	const n = 64
	metas := make([]*modelMeta, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			metas[i] = getMetaByType(typ)
		}(i)
	}
	wg.Wait()
	for i, m := range metas {
		if m != metas[0] {
			t.Fatalf("并发取同一类型应返回同一份元数据，第 %d 个不同", i)
		}
	}
	if !metas[0].tagChecked.Load() {
		t.Fatal("并发补校验后 tagChecked 应为 true")
	}
}

// ---- 7. BatchInsert / BatchUpsert 自动分片 ----

// recExec 记录每一条真正执行过的 SQL（mock driver 只留最后一条，看不出分批）。
type recExec struct {
	mu   sync.Mutex
	sqls []string
}

func (e *recExec) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	e.mu.Lock()
	e.sqls = append(e.sqls, query)
	e.mu.Unlock()
	return &stubResult{}, nil
}
func (e *recExec) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return nil, nil
}
func (e *recExec) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return nil
}

func (e *recExec) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.sqls...)
}

// TestBatchRowsPerStmtRespectsDialectLimit 每批行数由「方言参数上限 ÷ 列数」算出，
// 不是拍脑袋的常数（SQLite 32766 变量、PG / MySQL 65535 参数）。
func TestBatchRowsPerStmtRespectsDialectLimit(t *testing.T) {
	cases := []struct {
		d    Dialect
		cols int
		want int
	}{
		{SQLite, 2, (32766 - 8) / 2},
		{PG, 2, (65535 - 8) / 2},
		{MySQL, 6, (65535 - 8) / 6},
		{SQLite, 0, 1}, // 兜底：列数为 0 时也要能推进
	}
	for _, c := range cases {
		if got := batchRowsPerStmt(c.d, c.cols); got != c.want {
			t.Fatalf("batchRowsPerStmt(%v, %d) = %d，期望 %d", c.d, c.cols, got, c.want)
		}
	}
}

// TestBatchInsertSplitsWhenExceedingParamLimit 行数 × 列数越过上限时必须分批，
// 否则一条语句带几万个占位符，数据库直接硬报错。
func TestBatchInsertSplitsWhenExceedingParamLimit(t *testing.T) {
	ctx := context.Background()
	// 行数刻意写成常数而不是「per + 21」：万一每批行数的计算被改坏（比如放大成一亿），
	// 按它算出来的行数会把这条用例变成 OOM。20000 行 × 2 列超过 SQLite 的
	// 参数上限（2 列时每批 16379 行），足以触发分批，且不依赖被测函数的返回值。
	const total = 20000
	per := batchRowsPerStmt(SQLite, 2) // User 的可写列是 name / age（id 自增被排除）
	ents := make([]User, total)
	for i := range ents {
		ents[i] = User{Name: "n", Age: i}
	}
	exec := &recExec{}
	db := NewDB(exec, SQLite)
	if err := BatchInsert(ctx, db, ents); err != nil {
		t.Fatalf("BatchInsert 失败: %v", err)
	}
	sqls := exec.snapshot()
	if len(sqls) != 2 {
		t.Fatalf("应分成 2 批执行，实际 %d 批", len(sqls))
	}
	// 数行数要跳过列清单那对括号，只数 VALUES 之后的部分。
	rows := func(s string) int {
		i := strings.Index(s, "VALUES ")
		if i < 0 {
			return -1
		}
		return strings.Count(s[i:], "(")
	}
	if got := rows(sqls[0]); got != per {
		t.Fatalf("第一批应带满 %d 行，实际 %d", per, got)
	}
	if got := rows(sqls[1]); got != total-per {
		t.Fatalf("第二批应带剩下的 %d 行，实际 %d", total-per, got)
	}
}

func TestBatchUpsertSplitsWhenExceedingParamLimit(t *testing.T) {
	ctx := context.Background()
	// 同上：行数写成常数，避免被测函数的返回值一变就把用例拖成 OOM。
	const total = 20000
	ents := make([]User, total)
	exec := &recExec{}
	db := NewDB(exec, SQLite)
	if err := BatchUpsert(ctx, db, ents, nil); err != nil {
		t.Fatalf("BatchUpsert 失败: %v", err)
	}
	if got := len(exec.snapshot()); got != 2 {
		t.Fatalf("BatchUpsert 应分成 2 批，实际 %d", got)
	}
}

// ---- 8. Limit / Offset 负数 ----

func TestLimitOffsetRejectNegative(t *testing.T) {
	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("%s：应 panic", name)
			}
		}()
		fn()
	}
	// 负 LIMIT 在方言间语义相反：PG / MySQL 报错，SQLite 解释为无上限 ——
	// 此前框架一律不生成 LIMIT，于是 Limit(-1) 看起来设了限制、实际返回全表。
	mustPanic("Limit(-1)", func() { NewQuery[User]().Limit(-1) })
	mustPanic("Offset(-1)", func() { NewQuery[User]().Offset(-1) })
}

// ---- 附：Count 带 HAVING 但无 GROUP BY ----

// TestCountWithHavingButNoGroupByUsesSubquery 快速路径是单行 COUNT(*)，
// 把 HAVING 挂在它上面既丢过滤（返回 1 或 0 行），又产出含义不明的 SQL。
func TestCountWithHavingButNoGroupByUsesSubquery(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t) // SQLite
	if _, err := Count(ctx, db, NewQuery[Post]().HavingCount(">", 1)); err != nil {
		t.Fatalf("Count 失败: %v", err)
	}
	if !strings.Contains(recQuery, "COUNT(*) FROM (") {
		t.Fatalf("带 HAVING 的计数必须包子查询: %s", recQuery)
	}
	// 内层带 HAVING 时投影只能是常量：SELECT * 与聚合 HAVING 在 PG / MySQL
	// （ONLY_FULL_GROUP_BY）下是非法组合。
	if !strings.Contains(recQuery, "SELECT 1 FROM") {
		t.Fatalf("内层应投影常量 1: %s", recQuery)
	}
	if !strings.Contains(recQuery, "HAVING COUNT(*)") {
		t.Fatalf("HAVING 必须被带进计数: %s", recQuery)
	}
}
