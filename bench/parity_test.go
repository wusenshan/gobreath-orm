package bench

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	orm "github.com/wusenshan/gobreath-orm"
	"gorm.io/gorm"
)

// TestResultParity 断言三层在同一输入下**返回完全相同的数据**。
//
// 这是整套基准的前提：只有三层的查询语义真的等价，后面那些 ns/op 才有可比性。
// 一旦有人改了某一层的 WHERE / ORDER BY / 软删除行为，这个测试会先炸，
// 而不是让基准悄悄地在比「不同的活」。
func TestResultParity(t *testing.T) {
	ctx := context.Background()
	rawH := newRawHarness(t)
	gormH := newGormHarness(t)
	ormH := newOrmHarness(t)

	t.Run("SelectByID", func(t *testing.T) {
		rawGot, err := rawH.selectByID(ctx, targetID)
		if err != nil {
			t.Fatalf("raw: %v", err)
		}
		gormGot, err := gormH.selectByID(ctx, targetID)
		if err != nil {
			t.Fatalf("gorm: %v", err)
		}
		ormGot, err := ormH.selectByID(ctx, targetID)
		if err != nil {
			t.Fatalf("gobreath: %v", err)
		}

		if rawGot.ID != gormGot.ID || rawGot.ID != ormGot.ID {
			t.Fatalf("主键不一致：raw=%d gorm=%d gobreath=%d", rawGot.ID, gormGot.ID, ormGot.ID)
		}
		if rawGot.Name != gormGot.Name || rawGot.Name != ormGot.Name {
			t.Fatalf("name 不一致：raw=%q gorm=%q gobreath=%q", rawGot.Name, gormGot.Name, ormGot.Name)
		}
		if rawGot.Age != gormGot.Age || rawGot.Age != ormGot.Age {
			t.Fatalf("age 不一致：raw=%d gorm=%d gobreath=%d", rawGot.Age, gormGot.Age, ormGot.Age)
		}
		if rawGot.City != gormGot.City || rawGot.City != ormGot.City {
			t.Fatalf("city 不一致：raw=%q gorm=%q gobreath=%q", rawGot.City, gormGot.City, ormGot.City)
		}
		if rawGot.Score != gormGot.Score || rawGot.Score != ormGot.Score {
			t.Fatalf("score 不一致：raw=%v gorm=%v gobreath=%v", rawGot.Score, gormGot.Score, ormGot.Score)
		}
		if !rawGot.CreatedAt.Equal(gormGot.CreatedAt) || !rawGot.CreatedAt.Equal(ormGot.CreatedAt) {
			t.Fatalf("created_at 不一致：raw=%v gorm=%v gobreath=%v",
				rawGot.CreatedAt, gormGot.CreatedAt, ormGot.CreatedAt)
		}
	})

	t.Run("ListPage", func(t *testing.T) {
		rawList, err := rawH.listPage(ctx, queryCity, queryMinAge, queryLimit, queryOffset)
		if err != nil {
			t.Fatalf("raw: %v", err)
		}
		gormList, err := gormH.listPage(ctx, queryCity, queryMinAge, queryLimit, queryOffset)
		if err != nil {
			t.Fatalf("gorm: %v", err)
		}
		ormList, err := ormH.listPage(ctx, queryCity, queryMinAge, queryLimit, queryOffset)
		if err != nil {
			t.Fatalf("gobreath: %v", err)
		}

		if len(rawList) != queryLimit {
			t.Fatalf("raw 返回 %d 行，期望 %d 行（场景参数需调整）", len(rawList), queryLimit)
		}
		if len(gormList) != len(rawList) {
			t.Fatalf("行数不一致：raw=%d gorm=%d", len(rawList), len(gormList))
		}
		if len(ormList) != len(rawList) {
			t.Fatalf("行数不一致：raw=%d gobreath=%d", len(rawList), len(ormList))
		}
		for i := range rawList {
			if rawList[i].ID != gormList[i].ID {
				t.Fatalf("第 %d 行的 id 不一致：raw=%d gorm=%d", i, rawList[i].ID, gormList[i].ID)
			}
			if rawList[i].ID != ormList[i].ID {
				t.Fatalf("第 %d 行的 id 不一致：raw=%d gobreath=%d", i, rawList[i].ID, ormList[i].ID)
			}
		}
	})

	t.Run("Count", func(t *testing.T) {
		rawN, err := rawH.count(ctx, queryCity)
		if err != nil {
			t.Fatalf("raw: %v", err)
		}
		gormN, err := gormH.count(ctx, queryCity)
		if err != nil {
			t.Fatalf("gorm: %v", err)
		}
		ormN, err := ormH.count(ctx, queryCity)
		if err != nil {
			t.Fatalf("gobreath: %v", err)
		}
		if rawN != gormN || rawN != ormN {
			t.Fatalf("计数不一致：raw=%d gorm=%d gobreath=%d", rawN, gormN, ormN)
		}
		if rawN == 0 {
			t.Fatal("计数为 0，场景参数有问题")
		}
		t.Logf("city=%q 命中 %d 行", queryCity, rawN)
	})

	// UpdateByID 单独放在最后：它会改数据。前面几个读场景必须先在干净数据上跑完。
	t.Run("UpdateByID", func(t *testing.T) {
		rawSrc, err := rawH.selectByID(ctx, targetID)
		if err != nil {
			t.Fatalf("raw: %v", err)
		}
		if err := rawH.updateByID(ctx, updatePayload(targetID, rawSrc.CreatedAt)); err != nil {
			t.Fatalf("raw: %v", err)
		}

		gormSrc, err := gormH.selectByID(ctx, targetID)
		if err != nil {
			t.Fatalf("gorm: %v", err)
		}
		if err := gormH.updateByID(ctx, updatePayload(targetID, gormSrc.CreatedAt)); err != nil {
			t.Fatalf("gorm: %v", err)
		}

		ormSrc, err := ormH.selectByID(ctx, targetID)
		if err != nil {
			t.Fatalf("gobreath: %v", err)
		}
		if err := ormH.updateByID(ctx, updatePayload(targetID, ormSrc.CreatedAt)); err != nil {
			t.Fatalf("gobreath: %v", err)
		}

		rawAfter, err := rawH.selectByID(ctx, targetID)
		if err != nil {
			t.Fatalf("raw 回读: %v", err)
		}
		gormAfter, err := gormH.selectByID(ctx, targetID)
		if err != nil {
			t.Fatalf("gorm 回读: %v", err)
		}
		ormAfter, err := ormH.selectByID(ctx, targetID)
		if err != nil {
			t.Fatalf("gobreath 回读: %v", err)
		}

		if rawAfter.Name != gormAfter.Name || rawAfter.Name != ormAfter.Name {
			t.Fatalf("更新后 name 不一致：raw=%q gorm=%q gobreath=%q",
				rawAfter.Name, gormAfter.Name, ormAfter.Name)
		}
		if rawAfter.Age != gormAfter.Age || rawAfter.Age != ormAfter.Age {
			t.Fatalf("更新后 age 不一致：raw=%d gorm=%d gobreath=%d", rawAfter.Age, gormAfter.Age, ormAfter.Age)
		}
		if rawAfter.Score != gormAfter.Score || rawAfter.Score != ormAfter.Score {
			t.Fatalf("更新后 score 不一致：raw=%v gorm=%v gobreath=%v", rawAfter.Score, gormAfter.Score, ormAfter.Score)
		}
		// created_at 是这一组最容易出事的地方：gobreath 的 UpdateById 会重写它，
		// 如果 payload 没带原值，这里就会立刻显出 0001-01-01。
		if !rawAfter.CreatedAt.Equal(gormAfter.CreatedAt) || !rawAfter.CreatedAt.Equal(ormAfter.CreatedAt) {
			t.Fatalf("更新后 created_at 被改了：raw=%v gorm=%v gobreath=%v",
				rawAfter.CreatedAt, gormAfter.CreatedAt, ormAfter.CreatedAt)
		}
		if !rawAfter.UpdatedAt.Equal(gormAfter.UpdatedAt) || !rawAfter.UpdatedAt.Equal(ormAfter.UpdatedAt) {
			t.Fatalf("更新后 updated_at 不一致：raw=%v gorm=%v gobreath=%v",
				rawAfter.UpdatedAt, gormAfter.UpdatedAt, ormAfter.UpdatedAt)
		}
	})
}

// ---- 真实 SQL 抓取（仅供展示 / 排查） ----

// sqlRecorder 通过 gobreath 的日志钩子捕获**实际执行**的语句与绑定参数。
//
// 用它而不是 DryRun：DryRun 只能重现「用 Query 构造器拼出来的」SQL，
// 像 SelectById / Insert / UpdateById 这些走固定模板的路径它够不着；
// 而日志钩子拿到的就是真正下发到驱动的字符串。
//
// 只在 TestSQLParity 里启用 —— 基准测试绝不能开日志，I/O 会淹没被测开销。
type sqlRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *sqlRecorder) hook() orm.LogFunc {
	return func(_ orm.LogLevel, query string, args []any, _ time.Duration, err error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		line := query
		if len(args) > 0 {
			line += fmt.Sprintf("\n        args=%v", args)
		}
		if err != nil {
			line += fmt.Sprintf("\n        err=%v", err)
		}
		r.seen = append(r.seen, line)
	}
}

// reset 丢弃已捕获的语句（harness 构造阶段的建表与种子也会被捕获）。
func (r *sqlRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = nil
}

// drain 返回并清空已捕获的语句。
func (r *sqlRecorder) drain() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.seen
	r.seen = nil
	return out
}

// TestSQLParity 把三层实际执行的 SQL 打出来供人工复核。
//
//	cd bench && go test -run TestSQLParity -v
//
// 结果一致性由 TestResultParity 断言；这里只做「肉眼可查」的呈现，
// 因为不同 ORM 的 SQL 文本本来就不会逐字相同（占位符风格、标识符引号、LIMIT 写法各异）。
func TestSQLParity(t *testing.T) {
	ctx := context.Background()

	t.Run("SelectByID", func(t *testing.T) {
		t.Logf("raw:\n    %s", rawSelectByIDSQL)

		gormH := newGormHarness(t)
		tx := gormH.db.Session(&gorm.Session{DryRun: true})
		var u GormUser
		tx = tx.First(&u, targetID)
		t.Logf("gorm:\n    %s", tx.Statement.SQL.String())

		rec := &sqlRecorder{}
		ormH := newOrmHarnessWith(t, orm.Info, rec.hook())
		rec.reset()
		if _, err := ormH.selectByID(ctx, targetID); err != nil {
			t.Fatalf("gobreath: %v", err)
		}
		for _, s := range rec.drain() {
			t.Logf("gobreath 实际执行:\n    %s", s)
		}
	})

	t.Run("ListPage", func(t *testing.T) {
		t.Logf("raw:\n    %s", rawListPageSQL)

		gormH := newGormHarness(t)
		tx := gormH.db.Session(&gorm.Session{DryRun: true})
		var out []GormUser
		tx = tx.Where("city = ? AND age >= ?", queryCity, queryMinAge).
			Order("id").Limit(queryLimit).Offset(queryOffset).Find(&out)
		t.Logf("gorm:\n    %s", tx.Statement.SQL.String())

		rec := &sqlRecorder{}
		ormH := newOrmHarnessWith(t, orm.Info, rec.hook())
		rec.reset()
		if _, err := ormH.listPage(ctx, queryCity, queryMinAge, queryLimit, queryOffset); err != nil {
			t.Fatalf("gobreath: %v", err)
		}
		for _, s := range rec.drain() {
			t.Logf("gobreath 实际执行:\n    %s", s)
		}
	})

	t.Run("Count", func(t *testing.T) {
		t.Logf("raw:\n    %s", rawCountSQL)

		gormH := newGormHarness(t)
		tx := gormH.db.Session(&gorm.Session{DryRun: true})
		var n int64
		tx = tx.Model(&GormUser{}).Where("city = ?", queryCity).Count(&n)
		t.Logf("gorm:\n    %s", tx.Statement.SQL.String())

		rec := &sqlRecorder{}
		ormH := newOrmHarnessWith(t, orm.Info, rec.hook())
		rec.reset()
		if _, err := ormH.count(ctx, queryCity); err != nil {
			t.Fatalf("gobreath: %v", err)
		}
		for _, s := range rec.drain() {
			t.Logf("gobreath 实际执行:\n    %s", s)
		}
	})

	t.Run("Insert", func(t *testing.T) {
		t.Logf("raw:\n    %s", rawInsertSQL)

		rec := &sqlRecorder{}
		ormH := newOrmHarnessWith(t, orm.Info, rec.hook())
		rec.reset()
		if err := ormH.insertOne(ctx, makeRows(1)[0]); err != nil {
			t.Fatalf("gobreath: %v", err)
		}
		for _, s := range rec.drain() {
			t.Logf("gobreath 实际执行:\n    %s", s)
		}
	})

	t.Run("UpdateByID", func(t *testing.T) {
		t.Logf("raw:\n    %s", rawUpdateByIDSQL)

		rec := &sqlRecorder{}
		ormH := newOrmHarnessWith(t, orm.Info, rec.hook())
		src, err := ormH.selectByID(ctx, targetID)
		if err != nil {
			t.Fatalf("gobreath: %v", err)
		}
		rec.reset()
		if err := ormH.updateByID(ctx, updatePayload(targetID, src.CreatedAt)); err != nil {
			t.Fatalf("gobreath: %v", err)
		}
		for _, s := range rec.drain() {
			t.Logf("gobreath 实际执行:\n    %s", s)
		}
	})
}
