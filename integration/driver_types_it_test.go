package integration

import (
	"context"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// ShapeDoc 专门用来取「驱动交给 scan 的类型 ≠ Go 字段类型」这一类列。
//
// 为什么手工建表而不用 AutoMigrate：这里的关键是**服务端列类型**（numeric / uuid / text[]），
// 而 AutoMigrate 是由 Go 类型反推 DDL 的（float64 → double precision、string → text），
// 建不出 numeric / uuid，也就无法制造「服务端类型与字段类型错开」这个前提。
//
// 为什么需要它：框架自己分派类型的地方只有**模型字段扫描**（model.go 的
// assignString / assignFloat / assignInt ...）—— RawQuery/RawOne 的标量路径与
// SumOf/AvgOf 这类聚合都直接 rows.Scan，交给 database/sql 的 convertAssign。
// 而这两条驱动在这里给的类型并不一样（实测）：
//
//	服务端类型   pgx       lib/pq
//	----------   -------   --------
//	numeric      string    []byte
//	uuid         string    []byte
//	text[]       string    []byte
//	bytea        []byte    []byte   ← 对照组，两边一致
//	text         string    string   ← 对照组，两边一致
//
// 也就是说 assignFloat / assignString 的 `case []byte` 分支**只在 lib/pq 下会被走到**。
// 在此之前集成用例里能造出 []byte 载荷的模型列只有向量列（VecDoc.Emb），
// numeric / uuid / 数组这三类列一条都没有 —— 那几个分支等于没测过。
type ShapeDoc struct {
	ID   int64   `db:"id,pk"`
	Num  float64 `db:"num"`  // numeric → 字段 float64
	UID  string  `db:"uid"`  // uuid    → 字段 string
	Tags string  `db:"tags"` // text[]  → 字段 string（框架不做数组解析，原样给字面量）
	Blob []byte  `db:"blob"` // bytea   → 对照组
	Note string  `db:"note"` // text    → 对照组
}

func (ShapeDoc) TableName() string { return "it_shape_docs" }

const shapeUID = "00000000-0000-0000-0000-000000000001"

// TestModelScanAcrossDrivers 用模型路径读回上述列，断言两条驱动给出的**用户可见值一致**。
func TestModelScanAcrossDrivers(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		if b.dialect != orm.PG {
			// uuid / text[] 的 DDL 与字面量是 PG 专属语法；本用例的靶子是 PG 的两条驱动路径。
			t.Skipf("[%s] 本用例针对 PG 两条驱动的类型形态差异，非 PG 后端跳过", b.name)
		}
		ctx := context.Background()

		// 本表不在 harness 的 itTables 里（手工 DDL），因此不被 resetSchema 覆盖：
		// 入口先 DROP 保证幂等 —— 上一轮若在断言上 t.Fatalf 中断，收尾就轮不到执行。
		stmts := []string{
			`DROP TABLE IF EXISTS it_shape_docs`,
			`CREATE TABLE it_shape_docs (
				id   BIGSERIAL PRIMARY KEY,
				num  NUMERIC(10,2) NOT NULL,
				uid  UUID          NOT NULL,
				tags TEXT[]        NOT NULL,
				blob BYTEA         NOT NULL,
				note TEXT          NOT NULL
			)`,
			`INSERT INTO it_shape_docs (num, uid, tags, blob, note)
			 VALUES (1.5, '00000000-0000-0000-0000-000000000001', ARRAY['a','b'], '\x41', 'hi')`,
		}
		for _, s := range stmts {
			if _, err := orm.RawExec(ctx, db, s); err != nil {
				t.Fatalf("[%s] 准备 it_shape_docs 失败：%v\nSQL: %s", b.name, err, s)
			}
		}
		t.Cleanup(func() {
			_, _ = orm.RawExec(context.Background(), db, "DROP TABLE IF EXISTS it_shape_docs")
		})

		got, err := orm.SelectById[ShapeDoc](ctx, db, 1)
		if err != nil || got == nil {
			t.Fatalf("[%s] SelectById(ShapeDoc) 失败：%v", b.name, err)
		}

		// numeric → float64：pgx 走 string 分支、lib/pq 走 []byte 分支
		if got.Num != 1.5 {
			t.Errorf("[%s] numeric → float64 = %v；期望 1.5（驱动给出的原始形态不同，值必须一致）",
				b.name, got.Num)
		}
		// uuid → string：同上
		if got.UID != shapeUID {
			t.Errorf("[%s] uuid → string = %q；期望 %q", b.name, got.UID, shapeUID)
		}
		// text[] → string：框架不做数组解析，交出 PG 的数组字面量
		if got.Tags != "{a,b}" {
			t.Errorf("[%s] text[] → string = %q；期望 PG 数组字面量 %q", b.name, got.Tags, "{a,b}")
		}
		// 对照组
		if len(got.Blob) != 1 || got.Blob[0] != 0x41 {
			t.Errorf("[%s] bytea → []byte = %v；期望 [65]", b.name, got.Blob)
		}
		if got.Note != "hi" {
			t.Errorf("[%s] text → string = %q；期望 \"hi\"", b.name, got.Note)
		}

		t.Logf("[%s] 模型路径跨驱动读回一致：numeric=%.2f uuid=%s text[]=%s bytea=%v text=%q",
			b.name, got.Num, got.UID, got.Tags, got.Blob, got.Note)
	})
}
