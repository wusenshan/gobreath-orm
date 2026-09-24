package orm

import (
	"context"
	"strings"
	"testing"
)

// reuseRow 刻意不实现 TableName()：表名走自动推导，DB 表前缀才会真正参与拼接。
// 显式指定表名的模型会绕过前缀（applyPrefix 对 explicit 直接返回原名），
// 拿它测「前缀是否泄漏到调用方」会得到假性通过。
type reuseRow struct {
	Id   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
}

var (
	reuseName  = Col[reuseRow](func(r *reuseRow) *string { return &r.Name })
	reuseTName = TCol(func(r *reuseRow) *string { return &r.Name })
)

// applyLogic 的契约是「返回一个新查询」，因为调用方紧接着链的是**就地修改**的
// WithDialect / WithPrefix / Limit。只要它在短路径上把原对象还回来，这些写入
// 就会落到调用方的 Query 上：调用方从此被上一次查询的方言 / 表前缀 / LIMIT 粘住。
//
// 短路径有两种：模型没有生效的逻辑删除列、以及调用方已显式 Unscoped。
func TestApplyLogicAlwaysReturnsFreshQuery(t *testing.T) {
	db := newMockDB(t)

	t.Run("无软删除列", func(t *testing.T) {
		q := NewQuery[reuseRow]().Eq(reuseName, "a")
		if got := q.applyLogic(getMeta[reuseRow](), db); got == q {
			t.Fatal("无软删除列时 applyLogic 把调用方的 Query 原样返回了，后续链式写入会污染调用方")
		}
	})

	t.Run("已 Unscoped", func(t *testing.T) {
		q := NewQuery[reuseRow]().Unscoped()
		if got := q.applyLogic(getMeta[reuseRow](), db); got == q {
			t.Fatal("Unscoped 时 applyLogic 把调用方的 Query 原样返回了，后续链式写入会污染调用方")
		}
	})

	t.Run("软删除列生效", func(t *testing.T) {
		q := NewQuery[SoftUser]()
		if got := q.applyLogic(getMeta[SoftUser](), db); got == q {
			t.Fatal("软删除列生效时 applyLogic 把调用方的 Query 原样返回了")
		}
	})
}

// SelectOne 内部需要取首条，会给查询补 LIMIT 1。这个 LIMIT 必须只作用于内部副本：
// 一旦写回调用方，之后复用同一个 Query 做列表查询就会**静默只拿到 1 行**。
func TestSelectOneDoesNotPinLimitOnCallerQuery(t *testing.T) {
	db := newMockDB(t)
	ctx := context.Background()

	q := NewQuery[reuseRow]().Eq(reuseName, "a")
	recQuery = ""
	if _, err := SelectOne[reuseRow](ctx, db, q); err != nil && err != ErrNotFound {
		t.Fatalf("SelectOne 失败：%v", err)
	}
	// 先证明 LIMIT 1 确实用过 —— 否则下面的断言可能只是因为压根没生成 LIMIT 而空过。
	if !strings.Contains(strings.ToUpper(recQuery), "LIMIT 1") {
		t.Fatalf("前置条件不成立：SelectOne 未生成 LIMIT 1，用例失去意义：%s", recQuery)
	}
	if sqlStr, _ := q.ToSQL(); strings.Contains(strings.ToUpper(sqlStr), "LIMIT") {
		t.Fatalf("SelectOne 把 LIMIT 写回了调用方的 Query，复用它查列表会静默少取数据：%s", sqlStr)
	}
}

// 所有查询入口都必须在**内部副本**上补 db 上下文（方言 / 表前缀）。
// 否则同一个 Query 先后用于两个 db 时会带上上一次的上下文：
//   - 方言粘住 → ToSQL 的引号 / 占位符与本查询自身状态不符；
//   - 前缀粘住 → ToSQL（文档承诺「不会补上 db 级表前缀」）凭空多出前缀。
func TestQueryEntrypointsDoNotStampDBContextOnCallerQuery(t *testing.T) {
	ctx := context.Background()
	// mock 是 SQLite 方言，而 NewQuery 默认 PG；再叠一个表前缀，两种泄漏都可观测。
	db := newMockDB(t).WithPrefix("t_")

	entries := []struct {
		name string
		// run 执行入口并回报「内部真正使用的 SQL」，供前置断言确认用例没空跑。
		run func(t *testing.T, q *Query[reuseRow]) (sqlStr string)
	}{
		{"SelectList", func(t *testing.T, q *Query[reuseRow]) string {
			_, _ = SelectList[reuseRow](ctx, db, q)
			return recQuery
		}},
		{"SelectOne", func(t *testing.T, q *Query[reuseRow]) string {
			_, _ = SelectOne[reuseRow](ctx, db, q)
			return recQuery
		}},
		{"Count", func(t *testing.T, q *Query[reuseRow]) string {
			_, _ = Count[reuseRow](ctx, db, q)
			return recQuery
		}},
		{"Exists", func(t *testing.T, q *Query[reuseRow]) string {
			_, _ = Exists[reuseRow](ctx, db, q)
			return recQuery
		}},
		{"Pluck", func(t *testing.T, q *Query[reuseRow]) string {
			_, _ = Pluck[reuseRow, string](ctx, db, q, reuseTName)
			return recQuery
		}},
		{"Page", func(t *testing.T, q *Query[reuseRow]) string {
			_, _ = Page[reuseRow](ctx, db, q, 1, 2)
			return recQuery
		}},
		{"DryRun", func(t *testing.T, q *Query[reuseRow]) string {
			sqlStr, _ := DryRun[reuseRow](db, q)
			return sqlStr
		}},
	}

	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			q := NewQuery[reuseRow]().Eq(reuseName, "a")
			want, _ := q.ToSQL()

			recQuery = ""
			inner := e.run(t, q)
			if !strings.Contains(inner, "t_reuse_rows") {
				t.Fatalf("前置条件不成立：内部执行的 SQL 未带 db 前缀，用例无意义：%s", inner)
			}
			if got, _ := q.ToSQL(); got != want {
				t.Fatalf("%s 把 db 上下文写回了调用方的 Query：\n入口前: %s\n入口后: %s", e.name, want, got)
			}
		})
	}
}
