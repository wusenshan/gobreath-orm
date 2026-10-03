package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 本文件是第四轮走查的回归用例，覆盖一批「tag 修饰符 / 约定字段名 与字段类型不配套」
// 导致的静默错误 —— 它们的共同形状是：修饰符决定 SQL 语义，却没人校验字段类型。
//
//  1. `,logic` 打在类型不支持的字段上 → WHERE "state" = 0，MySQL / SQLite 静默返回 0 行
//  2. 约定软删除字段名命中类型不支持的字段 → 软删除静默不启用
//  3. 约定乐观锁字段名命中非整型字段 → SET "ver" = "ver" + 1，MySQL 静默转 0 再 +1
//  4. `,vector` 打在非切片字段上 → DDL 照样建 vector(N) 列，写入时才炸
//  5. `,autoincrement` 打在非整型主键上 → 自增回填静默跳过，主键永远是零值
//
// 统一修法是**在模型解析期按字段类型校验修饰符**（validateFieldModifiers），
// 把「静默做错事」提前成启动期 panic，与 compositePKPanic / validateDbTag 同一口径。
// 约定字段名（Config.SoftDeleteField / OptimisticField）路径在 resolveLogic /
// resolveVersion 里同样硬失败，逃生口分别是 `,nologic` 与 `,noversion`。

// ---- 1. `,logic` 的字段类型 ----

// strLogic 软删除列是 string：生成 `WHERE "state" = 0` ——
// PG 报错，MySQL / SQLite 静默返回 0 行（SQLite 把解析不出的标识符兜底成字符串字面量）。
type strLogic struct {
	Id    int64  `db:"id,pk,autoincrement"`
	Name  string `db:"name"`
	State string `db:"state,logic"`
}

func TestLogicTagRejectsUnsupportedType(t *testing.T) {
	assertParsePanics(t, reflect.TypeOf(strLogic{}), "logic",
		"软删除列打在 string 字段上必须 panic：此前生成 WHERE \"state\" = 0，MySQL / SQLite 静默查不出数据")
}

// okLogicTime / okLogicBool / okLogicInt 是三种受支持的软删除列类型（含指针形式）。
type okLogicTime struct {
	Id        int64      `db:"id,pk,autoincrement"`
	DeletedAt *time.Time `db:"deleted_at,logic"`
}
type okLogicBool struct {
	Id      int64 `db:"id,pk,autoincrement"`
	Deleted bool  `db:"deleted,logic"`
}
type okLogicInt struct {
	Id     int64 `db:"id,pk,autoincrement"`
	Status int   `db:"status,logic"`
}

// TestLogicTagAcceptsSupportedTypes 合法类型不能被上面的闸门误伤，且判定式要随类型变化。
func TestLogicTagAcceptsSupportedTypes(t *testing.T) {
	db := mockDB(t, SQLite)
	cases := []struct {
		typ   reflect.Type
		want  string
		label string
	}{
		{reflect.TypeOf(okLogicTime{}), `"deleted_at" IS NULL`, "time"},
		{reflect.TypeOf(okLogicBool{}), `"deleted" = false`, "bool"},
		{reflect.TypeOf(okLogicInt{}), `"status" = 0`, "int"},
	}
	for _, c := range cases {
		li := resolveLogic(getMetaByType(c.typ), db)
		if li == nil {
			t.Fatalf("[%s] 合法软删除列应被识别，实际未启用", c.label)
		}
		if got := logicSuffix(li, SQLite, false); got != c.want {
			t.Fatalf("[%s] 未删除判定应为 %s，实际 %s", c.label, c.want, got)
		}
	}
}

// ---- 2. 约定软删除字段名（Config.SoftDeleteField）----

type convSoft struct {
	Id    int64  `db:"id"`
	Name  string `db:"name"`
	State string `db:"state"`
}

// TestConventionSoftDeleteRejectsUnsupportedType 全局约定名命中了同名但类型不支持的字段：
// 此前静默不启用软删除（表里的软删除条件永远不生效，删掉的行照常出现在结果里）。
func TestConventionSoftDeleteRejectsUnsupportedType(t *testing.T) {
	db := mockDB(t, SQLite)
	db.softDeleteField = "state"
	assertFnPanics(t, "logic", func() {
		resolveLogic(getMetaByType(reflect.TypeOf(convSoft{})), db)
	}, "约定软删除名命中 string 字段应 panic，此前静默不启用软删除")
}

// convSoftOptOut 用 ,nologic 局部退出约定匹配（这条是上面 panic 的逃生口）。
type convSoftOptOut struct {
	Id    int64  `db:"id"`
	Name  string `db:"name"`
	State string `db:"state,nologic"`
}

func TestNologicOptsOutOfConvention(t *testing.T) {
	db := mockDB(t, SQLite)
	db.softDeleteField = "state"
	if li := resolveLogic(getMetaByType(reflect.TypeOf(convSoftOptOut{})), db); li != nil {
		t.Fatalf("加了 ,nologic 就该退出约定匹配，实际仍启用在 %q 上", li.col)
	}
}

// ---- 3. 约定乐观锁字段名（Config.OptimisticField）----

type convVer struct {
	Id      int64  `db:"id"`
	Name    string `db:"name"`
	Version string `db:"version"`
}

// TestConventionOptimisticRejectsNonInteger 约定名命中 string 版本列：
// 此前生成 SET "version" = "version" + 1 —— MySQL 静默把字符串转成 0 再 +1，冲突检测形同虚设。
func TestConventionOptimisticRejectsNonInteger(t *testing.T) {
	db := mockDB(t, SQLite)
	db.optimisticField = "version"
	assertFnPanics(t, "整型", func() {
		resolveVersion(getMetaByType(reflect.TypeOf(convVer{})), db)
	}, "约定乐观锁名命中 string 字段应 panic")
}

type convVerOptOut struct {
	Id      int64  `db:"id"`
	Name    string `db:"name"`
	Version string `db:"version,noversion"`
}

func TestNoversionOptsOutOfConvention(t *testing.T) {
	db := mockDB(t, SQLite)
	db.optimisticField = "version"
	if vi := resolveVersion(getMetaByType(reflect.TypeOf(convVerOptOut{})), db); vi != nil {
		t.Fatalf("加了 ,noversion 就该退出约定匹配，实际仍启用在 %q 上", vi.colName)
	}
}

// ---- 4. `,vector` 的字段类型 ----

type intVec struct {
	Id   int64 `db:"id,pk,autoincrement"`
	Name string
	Emb  int `db:"emb,vector(3)"`
}

func TestVectorTagRejectsNonSlice(t *testing.T) {
	assertParsePanics(t, reflect.TypeOf(intVec{}), "vector",
		"向量列打在 int 字段上必须 panic：此前 DDL 照样建 vector(3) 列，写入时才炸（SQLite 静默）")
}

type ptrVec struct {
	Id  int64      `db:"id,pk,autoincrement"`
	Emb *[]float32 `db:"emb,vector(3)"`
}
type strVec struct {
	Id  int64  `db:"id,pk,autoincrement"`
	Emb string `db:"emb,vector(3)"`
}
type intSliceVec struct {
	Id  int64  `db:"id,pk,autoincrement"`
	Emb []int  `db:"emb,vector(3)"`
	Arr [3]int `db:"arr,vector(3)"`
}

// TestVectorTagAcceptsSupportedTypes 合法向量类型不能被误伤：
// []float32 / string（文本形式）/ 数值切片 / 定长数组，含指针形式。
func TestVectorTagAcceptsSupportedTypes(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(Article{}),
		reflect.TypeOf(ptrVec{}),
		reflect.TypeOf(strVec{}),
		reflect.TypeOf(intSliceVec{}),
	} {
		if m := getMetaByType(typ); m == nil {
			t.Fatalf("%s 应能正常解析", typ)
		}
	}
}

// ---- 5. `,autoincrement` 的字段类型 ----

type strPK struct {
	Id   string `db:"id,pk,autoincrement"`
	Name string
}

func TestAutoIncrementRejectsNonIntegerPK(t *testing.T) {
	assertParsePanics(t, reflect.TypeOf(strPK{}), "autoincrement",
		"自增主键是 string 时必须 panic：LastInsertId 返回 int64 无法回填，此前静默留零值")
}

// TestSetFieldValueRejectsNonInteger 解析期闸门之外，回填函数自身也要拒绝非整型：
// 静默跳过会让后续 UpdateById / DeleteById 拿着零值主键操作。
func TestSetFieldValueRejectsNonInteger(t *testing.T) {
	e := &strPK{Id: "x"}
	fv := reflect.ValueOf(e).Elem().Field(0)
	if err := setFieldValue(fv, 7); err == nil {
		t.Fatal("回填非整型主键应报错，不能静默跳过")
	} else if !strings.Contains(err.Error(), "int64") {
		t.Fatalf("错误信息应说明无法承接 int64，实际: %v", err)
	}
	// 整型照常回填
	var n int64
	if err := setFieldValue(reflect.ValueOf(&n).Elem(), 7); err != nil || n != 7 {
		t.Fatalf("整型回填应成功: n=%d err=%v", n, err)
	}
}

// ---- 7. UpdateSets 的 SET 列顺序必须确定 ----

// TestUpdateSetsColumnOrderIsDeterministic sets 是 map，直接遍历会得到随机列顺序：
// 同一份代码每次生成的 SQL 都可能不同 —— 依赖 prepared statement 的驱动（MySQL / PG）
// 因此每次都要重新 Prepare，语句缓存命中率归零；日志与逐字比对 SQL 的断言也不稳定。
func TestUpdateSetsColumnOrderIsDeterministic(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, SQLite)
	sets := map[string]any{"name": "n", "age": 30, "score": 1.5, "title": "t"}
	seen := make(map[string]struct{})
	for i := 0; i < 20; i++ {
		q := NewQuery[audit4User]().Eq(Col[audit4User](func(u *audit4User) *int64 { return &u.Id }), int64(1))
		if _, err := UpdatePartial[audit4User](ctx, db, q, sets); err != nil {
			t.Fatalf("UpdatePartial 失败: %v", err)
		}
		seen[recQuery] = struct{}{}
	}
	if len(seen) != 1 {
		var all []string
		for s := range seen {
			all = append(all, s)
		}
		t.Fatalf("同一份 sets 应始终生成逐字相同的 SQL，实际出现了 %d 种:\n%s",
			len(seen), strings.Join(all, "\n"))
	}
	if !strings.Contains(recQuery, `SET "age" = ?, "name" = ?, "score" = ?, "title" = ?`) {
		t.Fatalf("SET 列应按列名排序，实际: %s", recQuery)
	}
}

type audit4User struct {
	Id    int64   `db:"id,pk,autoincrement"`
	Name  string  `db:"name"`
	Age   int     `db:"age"`
	Score float64 `db:"score"`
	Title string  `db:"title"`
}

// ---- 断言辅助 ----

// assertParsePanics 断言「解析该模型的元数据」会 panic，且 panic 信息含 want。
func assertParsePanics(t *testing.T, typ reflect.Type, want, why string) {
	t.Helper()
	assertFnPanics(t, want, func() { getMetaByType(typ) }, why)
}

func assertFnPanics(t *testing.T, want string, fn func(), why string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("%s（未 panic）", why)
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, want) {
			t.Fatalf("panic 信息应包含 %q，实际: %s", want, msg)
		}
	}()
	fn()
}

// ---- 6. 合法模型不能被这一批闸门误伤 ----

type healthyModel struct {
	Id        int64      `db:"id,pk,autoincrement"`
	Name      string     `db:"name"`
	Deleted   bool       `db:"deleted,logic"`
	Version   int        `db:"version,version"`
	Emb       []float32  `db:"emb,vector(3)"`
	CreatedAt *time.Time `db:"created_at"`
}

func TestHealthyModelSurvivesModifierChecks(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, SQLite)
	meta := getMeta[healthyModel]()
	if meta.logicCol == nil || meta.versionCol == nil || meta.pk == nil {
		t.Fatalf("合法模型的 logic / version / pk 都应解析出来: %+v", meta)
	}
	// 软删除条件照常追加，且判定式随类型（bool）正确
	if _, err := SelectList(ctx, db, NewQuery[healthyModel]()); err != nil {
		t.Fatalf("SelectList 失败: %v", err)
	}
	if !strings.Contains(recQuery, `"deleted" = false`) {
		t.Fatalf("查询应带软删除条件，实际: %s", recQuery)
	}
	// 乐观锁照常自增（而非把实体版本原样写回）
	e := &healthyModel{Id: 1, Name: "n", Version: 5}
	if err := UpdateById(ctx, db, e); err != nil {
		t.Fatalf("UpdateById 失败: %v", err)
	}
	if !strings.Contains(recQuery, `"version" = "version" + 1`) {
		t.Fatalf("版本应自增，实际: %s", recQuery)
	}
	if e.Version != 6 {
		t.Fatalf("实体版本应回写成 6，实际 %d", e.Version)
	}
}
