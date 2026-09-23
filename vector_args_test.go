package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ---- 占位符 / 参数一致性断言基建 ----

var rePGPlaceholder = regexp.MustCompile(`\$(\d+)`)

// countPlaceholders 统计 SQL 里的占位符个数。
//
// PG 的占位符是 $1..$n（Build 按渲染顺序逐个分配，不会有空洞），取最大序号即个数；
// MySQL / SQLite 一律是 "?"，直接数出现次数。
func countPlaceholders(t *testing.T, sqlStr string, d Dialect) int {
	t.Helper()
	switch d.(type) {
	case postgresDialect, *postgresDialect:
		maxN := 0
		for _, m := range rePGPlaceholder.FindAllStringSubmatch(sqlStr, -1) {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("解析 PG 占位符失败: %v（SQL: %s）", err, sqlStr)
			}
			if n > maxN {
				maxN = n
			}
		}
		return maxN
	default:
		return strings.Count(sqlStr, "?")
	}
}

// mockAggRow 让聚合查询能扫回一行值。
//
// Count / Exists / Pluck 在空结果集上就能正常返回（不注册即可），
// 但 Sum / Avg 走 queryRow + Scan，空结果集是 "sql: no rows in result set"。
func mockAggRow(t *testing.T, key string, v driver.Value) {
	t.Helper()
	prev, had := mockFactories[key]
	mockFactories[key] = func() driver.Rows {
		return &mockRows{cols: []string{"agg"}, data: [][]driver.Value{{v}}}
	}
	t.Cleanup(func() {
		if had {
			mockFactories[key] = prev
		} else {
			delete(mockFactories, key)
		}
	})
}

// ---------------------------------------------------------------- 严格 mock 驱动

// 真实驱动会主动告诉 database/sql「这条语句要几个参数」，后者在
// driverArgsConnLocked 里拿 len(args) 与它比对，不等就直接报
// "sql: expected N arguments, got M"（Go 标准库 database/sql/convert.go）。
//
// 本项目原有的 ormmock 驱动 NumInput() 恒返回 -1（等于放弃校验），于是
// 「SQL 里 3 个 ?、只传 2 个参数」这种错误在 mock 上完全不可见 —— MySQL 的
// 向量路径就是这么漏掉的。这组严格驱动按真实规则实现 NumInput：
//
//   - 位置型方言（MySQL / SQLite）：? 的出现次数。
//     对齐 go-sql-driver/mysql 的 mysqlStmt.NumInput() → paramCount
//     （即 countParams 数出的 ? 个数）。
//   - 引用型方言（PG）：$n 的最大序号 —— 同一个 $1 出现两次仍只占一个参数。
//
// 局限：不做引号感知，SQL 字面量里的 ? / $n 会被计入。本项目生成的语句里字面量
// 只可能来自 Last() / RawQuery，本文件不涉及。
type strictDriver struct{ positional bool }

func (d strictDriver) Open(string) (driver.Conn, error) {
	return &strictConn{positional: d.positional}, nil
}

type strictConn struct{ positional bool }

func (c *strictConn) Prepare(query string) (driver.Stmt, error) {
	return &strictStmt{query: query, numInput: strictNumInput(query, c.positional)}, nil
}
func (c *strictConn) Close() error              { return nil }
func (c *strictConn) Begin() (driver.Tx, error) { return &mockTx{}, nil }

type strictStmt struct {
	query    string
	numInput int
}

func (s *strictStmt) Close() error  { return nil }
func (s *strictStmt) NumInput() int { return s.numInput }
func (s *strictStmt) Exec(args []driver.Value) (driver.Result, error) {
	recQuery, recArgs = s.query, valuesToAny(args)
	return mockResult{}, nil
}
func (s *strictStmt) Query(args []driver.Value) (driver.Rows, error) {
	recQuery, recArgs = s.query, valuesToAny(args)
	return mockRowsFor(s.query), nil
}

func strictNumInput(query string, positional bool) int {
	if positional {
		return strings.Count(query, "?")
	}
	maxN := 0
	for _, m := range rePGPlaceholder.FindAllStringSubmatch(query, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > maxN {
			maxN = n
		}
	}
	return maxN
}

func init() {
	sql.Register("ormmock-strict-positional", strictDriver{positional: true})
	sql.Register("ormmock-strict-reference", strictDriver{positional: false})
}

// strictMockDB 按方言选一个严格 driver 建 DB：PG 用引用型规则，MySQL / SQLite
// 用位置型规则。判定直接复用框架自己的 positionalPlaceholder，避免测试与实现
// 各写一套判据。
func strictMockDB(t *testing.T, d Dialect) *DB {
	t.Helper()
	name := "ormmock-strict-reference"
	if positionalPlaceholder(d) {
		name = "ormmock-strict-positional"
	}
	sqlDB, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return NewDB(sqlDB, d)
}

// TestStrictMockEnforcesArgCount 先证明严格驱动**真的会拦**，否则上面那族用例
// 全绿也只是因为「压根没校验」这种最没价值的通过。
//
// 两个方向各验一次：位置型少传参数必须报错，引用型复用 $1 必须放行。
func TestStrictMockEnforcesArgCount(t *testing.T) {
	db := strictMockDB(t, MySQL)
	// 三个占位符、两个参数：必须被 database/sql 拦下。
	if _, err := db.queryContext(context.Background(),
		"SELECT * FROM `t` WHERE `a` = ? AND `b` = ? AND `c` = ?", 1, 2); err == nil {
		t.Fatal("参数个数与占位符不符时应当报错（严格驱动没生效？）")
	} else if !strings.Contains(err.Error(), "expected 3 arguments, got 2") {
		t.Fatalf("错误信息不符合预期: %v", err)
	}
	// PG 的引用型规则：$1 出现两次只算一个参数，应能通过。
	dbPG := strictMockDB(t, PG)
	if _, err := dbPG.queryContext(context.Background(),
		`SELECT *, "embedding" <-> $1 AS dist FROM "articles" ORDER BY "embedding" <-> $1`, "[1,2]"); err != nil {
		t.Fatalf("PG 引用型占位符复用同一参数不应报错: %v", err)
	}
}

// TestVectorArgsMatchPlaceholders 是一族回归用例，断言一条不变量：
//
//	SQL 里的占位符个数 == 传给驱动的参数个数
//
// 背景：Build() 原先在开头**无条件**把向量放进 args[0]，而渲染距离表达式的地方有三处
// （投影里的 dist 列 / 距离阈值过滤 / 按距离排序），各自由 noVecCol、vecFilterOn、
// “聚合时跳过排序”控制。三者可能同时是关的，于是：
//
//	orm.Count(ctx, db, NewQuery[Article]().Nearest(emb, vec, 5))
//
// 生成的 SQL 是 `SELECT COUNT(*) FROM "articles"` —— 零占位符，args 却带着一个向量。
// lib/pq 会直接报 "bind message supplies 1 parameters, but prepared statement requires 0"，
// MySQL / SQLite 的 prepared 路径同样失败。
//
// 这不是边缘场景：RAG 里「统计某向量邻域内有多少条」正好就是 Nearest + Count 的写法。
//
// 用例同时覆盖「别修过头」的方向：普通向量查询、Pluck、阈值过滤这些路径上，
// 向量参数必须照旧存在且仍是第一个（Where 的值不能被顶到 $2）。
func TestVectorArgsMatchPlaceholders(t *testing.T) {
	emb := func() ColExpr { return Col[Article](func(a *Article) *[]float32 { return &a.Embedding }) }
	titleCol := func() ColExpr { return Col[Article](func(a *Article) *string { return &a.Title }) }

	const vecStr = "[0.1,0.2,0.3]"
	vec := []float32{0.1, 0.2, 0.3}

	type runFn func(t *testing.T, ctx context.Context, db *DB, q *Query[Article]) (string, []any)

	// 走真实执行路径：SQL 与参数都取自 mock driver 记录下的那一份。
	exec := func(fn func(ctx context.Context, db *DB, q *Query[Article]) error) runFn {
		return func(t *testing.T, ctx context.Context, db *DB, q *Query[Article]) (string, []any) {
			t.Helper()
			if err := fn(ctx, db, q); err != nil {
				t.Fatalf("执行失败: %v", err)
			}
			return recQuery, recArgs
		}
	}
	buildOnly := func(t *testing.T, ctx context.Context, db *DB, q *Query[Article]) (string, []any) {
		t.Helper()
		return DryRun(db, q)
	}

	cases := []struct {
		name     string
		d        Dialect
		build    func() *Query[Article]
		run      runFn
		want     int // 期望占位符个数 == 期望参数个数
		checkSQL func(t *testing.T, sqlStr string)
		checkArg func(t *testing.T, args []any)
	}{
		// ---- 主回归：聚合 × 向量，向量参数不该存在 ----
		{
			name:  "PG Nearest+Count",
			d:     PG,
			build: func() *Query[Article] { return NewQuery[Article]().Nearest(emb(), vec, 5) },
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				_, err := Count(ctx, db, q)
				return err
			}),
			want: 0,
			checkSQL: func(t *testing.T, s string) {
				if strings.Contains(s, "ORDER BY") || strings.Contains(s, "LIMIT") {
					t.Fatalf("聚合查询不该带 ORDER BY / LIMIT: %s", s)
				}
			},
		},
		{
			name:  "PG Nearest+Exists",
			d:     PG,
			build: func() *Query[Article] { return NewQuery[Article]().Nearest(emb(), vec, 5) },
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				_, err := Exists(ctx, db, q)
				return err
			}),
			want: 0,
		},
		{
			name:  "PG Nearest+Sum",
			d:     PG,
			build: func() *Query[Article] { return NewQuery[Article]().Nearest(emb(), vec, 5) },
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				mockAggRow(t, `SUM("id")`, float64(6))
				_, err := Sum(ctx, db, q, Col[Article](func(a *Article) *int64 { return &a.Id }))
				return err
			}),
			want: 0,
		},
		{
			name:  "PG Nearest+Avg",
			d:     PG,
			build: func() *Query[Article] { return NewQuery[Article]().Nearest(emb(), vec, 5) },
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				mockAggRow(t, `AVG("id")`, float64(3))
				_, err := Avg(ctx, db, q, Col[Article](func(a *Article) *int64 { return &a.Id }))
				return err
			}),
			want: 0,
		},
		{
			name: "PG Nearest+Count+Where：WHERE 的值不能被顶到 $2",
			d:    PG,
			build: func() *Query[Article] {
				return NewQuery[Article]().Eq(titleCol(), "go").Nearest(emb(), vec, 5)
			},
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				_, err := Count(ctx, db, q)
				return err
			}),
			want: 1,
			checkSQL: func(t *testing.T, s string) {
				if !strings.Contains(s, `"title" = $1`) {
					t.Fatalf("WHERE 的值应占据 $1（向量参数已不存在）: %s", s)
				}
			},
			checkArg: func(t *testing.T, args []any) {
				if len(args) != 1 || args[0] != "go" {
					t.Fatalf("参数应只剩 WHERE 的值 [go]，实际 %v", args)
				}
			},
		},

		// ---- 阈值过滤要用向量：参数照旧，只是不能多也不能少 ----
		{
			name: "PG Nearest+WithinDistance+Count：阈值过滤仍需向量",
			d:    PG,
			build: func() *Query[Article] {
				return NewQuery[Article]().Nearest(emb(), vec, 5).WithinDistance(emb(), vec, 0.3)
			},
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				_, err := Count(ctx, db, q)
				return err
			}),
			want: 2,
			checkSQL: func(t *testing.T, s string) {
				if !strings.Contains(s, `"embedding" <-> $1 < $2`) {
					t.Fatalf("阈值过滤表达式错误: %s", s)
				}
			},
			checkArg: func(t *testing.T, args []any) {
				if len(args) != 2 || args[0] != vecStr || args[1] != float64(0.3) {
					t.Fatalf("参数应为 [向量 0.3]，实际 %v", args)
				}
			},
		},
		{
			name: "PG Nearest+WithinDistance+Count+Where",
			d:    PG,
			build: func() *Query[Article] {
				return NewQuery[Article]().Eq(titleCol(), "go").
					Nearest(emb(), vec, 5).WithinDistance(emb(), vec, 0.3)
			},
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				_, err := Count(ctx, db, q)
				return err
			}),
			want: 3,
			checkArg: func(t *testing.T, args []any) {
				// 参数顺序 = 渲染顺序：WHERE 的值先渲染，随后是阈值过滤里的向量与阈值。
				if len(args) != 3 || args[0] != "go" || args[1] != vecStr || args[2] != float64(0.3) {
					t.Fatalf("参数应为 [go 向量 0.3]，实际 %v", args)
				}
			},
		},
		{
			name: "PG Nearest+WithinDistance+Sum",
			d:    PG,
			build: func() *Query[Article] {
				return NewQuery[Article]().Nearest(emb(), vec, 5).WithinDistance(emb(), vec, 0.3)
			},
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				mockAggRow(t, `SUM("id")`, float64(6))
				_, err := Sum(ctx, db, q, Col[Article](func(a *Article) *int64 { return &a.Id }))
				return err
			}),
			want: 2,
		},

		// ---- 防止「修过头」：这些路径上向量参数必须还在 ----
		{
			name:  "PG Nearest+Pluck：按距离排序仍要向量",
			d:     PG,
			build: func() *Query[Article] { return NewQuery[Article]().Nearest(emb(), vec, 5) },
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				_, err := PluckCol[Article, string](ctx, db, q, titleCol())
				return err
			}),
			want: 1,
			checkSQL: func(t *testing.T, s string) {
				if !strings.Contains(s, `ORDER BY "embedding" <-> $1 ASC`) {
					t.Fatalf("Pluck 应保留按距离排序: %s", s)
				}
				if strings.Contains(s, "AS dist") {
					t.Fatalf("单列投影不该追加距离列: %s", s)
				}
			},
			checkArg: func(t *testing.T, args []any) {
				if len(args) != 1 || args[0] != vecStr {
					t.Fatalf("Pluck 应只带向量参数，实际 %v", args)
				}
			},
		},
		{
			name:  "PG 普通 Nearest 查询（回归保护）",
			d:     PG,
			build: func() *Query[Article] { return NewQuery[Article]().Nearest(emb(), vec, 5) },
			run:   buildOnly,
			want:  1,
			checkSQL: func(t *testing.T, s string) {
				if !strings.Contains(s, "AS dist") {
					t.Fatalf("普通向量查询应保留距离列: %s", s)
				}
			},
			checkArg: func(t *testing.T, args []any) {
				if len(args) != 1 || args[0] != vecStr {
					t.Fatalf("普通向量查询应只带向量参数，实际 %v", args)
				}
			},
		},
		{
			name: "PG Where+Nearest 普通查询：向量仍是 $1",
			d:    PG,
			build: func() *Query[Article] {
				return NewQuery[Article]().Eq(titleCol(), "go").Nearest(emb(), vec, 5)
			},
			run:  buildOnly,
			want: 2,
			checkSQL: func(t *testing.T, s string) {
				if !strings.Contains(s, `"embedding" <-> $1`) {
					t.Fatalf("dist 列应先用 $1 锚定向量: %s", s)
				}
			},
			checkArg: func(t *testing.T, args []any) {
				if len(args) != 2 || args[0] != vecStr || args[1] != "go" {
					t.Fatalf("参数应为 [向量 go]，实际 %v", args)
				}
			},
		},

		// ---- 另外两种方言（占位符是 ? 计数，语义同一套） ----
		{
			name:  "MySQL Nearest+Count",
			d:     MySQL,
			build: func() *Query[Article] { return NewQuery[Article]().Nearest(emb(), vec, 5) },
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				_, err := Count(ctx, db, q)
				return err
			}),
			want: 0,
		},
		{
			name: "MySQL Nearest+WithinDistance+Count+Where",
			d:    MySQL,
			build: func() *Query[Article] {
				return NewQuery[Article]().Eq(titleCol(), "go").
					Nearest(emb(), vec, 5).WithinDistance(emb(), vec, 0.3)
			},
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				_, err := Count(ctx, db, q)
				return err
			}),
			want: 3,
		},
		{
			name:  "MySQL 普通 Nearest 查询",
			d:     MySQL,
			build: func() *Query[Article] { return NewQuery[Article]().Nearest(emb(), vec, 5) },
			run:   buildOnly,
			want:  2, // dist 列 + ORDER BY 各一个 ?，故要两份向量
			checkSQL: func(t *testing.T, s string) {
				if !strings.Contains(s, "ORDER BY VECTOR_DISTANCE(`embedding`, STRING_TO_VECTOR(?), 'EUCLIDEAN') ASC") {
					t.Fatalf("MySQL 按距离排序表达式错误: %s", s)
				}
			},
			checkArg: func(t *testing.T, args []any) {
				if len(args) != 2 || args[0] != vecStr || args[1] != vecStr {
					t.Fatalf("位置型方言应传两份相同的向量，实际 %v", args)
				}
			},
		},
		{
			name:  "MySQL Nearest+SelectList：由驱动拦下参数不符",
			d:     MySQL,
			build: func() *Query[Article] { return NewQuery[Article]().Nearest(emb(), vec, 5) },
			// 走真实执行路径，这条用例的证据来自「驱动拒绝」而不是本文件的手写断言：
			// dist 列与 ORDER BY 各一个 ?，修复前只传 1 个向量，
			// database/sql 会报 "sql: expected 2 arguments, got 1"。
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				_, err := SelectList(ctx, db, q)
				return err
			}),
			want: 2,
		},
		{
			name:  "MySQL Nearest+Pluck：排序仍要向量（位置型不能复用）",
			d:     MySQL,
			build: func() *Query[Article] { return NewQuery[Article]().Nearest(emb(), vec, 5) },
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				_, err := PluckCol[Article, string](ctx, db, q, titleCol())
				return err
			}),
			want: 1, // 单列投影没有 dist 列，占位符只剩 ORDER BY 里那一个
			checkArg: func(t *testing.T, args []any) {
				if len(args) != 1 || args[0] != vecStr {
					t.Fatalf("Pluck 应只带一份向量，实际 %v", args)
				}
			},
		},
		{
			name:  "SQLite Nearest+Count",
			d:     SQLite,
			build: func() *Query[Article] { return NewQuery[Article]().Nearest(emb(), vec, 5) },
			run: exec(func(ctx context.Context, db *DB, q *Query[Article]) error {
				_, err := Count(ctx, db, q)
				return err
			}),
			want: 0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 用严格驱动：参数个数与占位符不符会由 database/sql 直接拦下，
			// 而不是靠下面那句手写断言才发现。
			db := strictMockDB(t, c.d)
			recQuery, recArgs = "", nil // 全局记录器，用前先清空

			sqlStr, args := c.run(t, context.Background(), db, c.build())

			got := countPlaceholders(t, sqlStr, c.d)
			if got != c.want {
				t.Fatalf("占位符个数 = %d，期望 %d\nSQL: %s", got, c.want, sqlStr)
			}
			if len(args) != c.want {
				t.Fatalf("参数个数 = %d（%v），期望 %d\nSQL: %s", len(args), args, c.want, sqlStr)
			}
			if c.checkSQL != nil {
				c.checkSQL(t, sqlStr)
			}
			if c.checkArg != nil {
				c.checkArg(t, args)
			}
		})
	}
}
