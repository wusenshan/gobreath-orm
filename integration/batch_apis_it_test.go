package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	orm "github.com/wusenshan/gobreath-orm"
)

// 批量按主键 API 与表自省的真库验证。
//
// 为什么必须真库：mock 层只能断言 SQL 字符串，而「位置型方言（MySQL / SQLite
// 的 ?）上 SET/IN 实参顺序错位」在字符串层面不可见 —— 占位符没有序号，
// `SET deleted_at = ? WHERE id IN (?, ?)` 配 [条件值, 删除值] 与正确配对逐字相同。
// 只有真库能把错位暴露成「静默 0 行」（历史上 Delete 真出过这事故）。
// mock 变异测试也证实了这一点：变异 idsIN 起始序号后只有 PG 子用例 FAIL
// （$n 有序号），MySQL / SQLite 照过 —— 所以本文件的软删用例是唯一能拦住它的 gate。

func TestBatchByIDApisOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db) // alice/bob/carol/dave，ID 自增回填

		// ---- SelectByIds：命中 2/4，IN 里混入不存在的 ID 不报错 ----
		got, err := orm.SelectByIds[User](ctx, db, users[0].ID, users[2].ID, int64(99999))
		if err != nil {
			t.Fatalf("[%s] SelectByIds 出错：%v", b.name, err)
		}
		if len(got) != 2 {
			t.Fatalf("[%s] SelectByIds 应命中 2 行，实际 %d", b.name, len(got))
		}
		if _, err := orm.SelectByIds[User](ctx, db); !errors.Is(err, orm.ErrInvalidArgument) {
			t.Fatalf("[%s] SelectByIds 空列表应 ErrInvalidArgument，实际 %v", b.name, err)
		}

		// ---- UpdateByIdsSets：批量部分更新，未列入的行不受影响 ----
		n, err := orm.UpdateByIdsSets[User](ctx, db,
			[]any{users[1].ID, users[3].ID}, map[string]any{"city": "sz", "age": 40})
		if err != nil || n != 2 {
			t.Fatalf("[%s] UpdateByIdsSets：n=%d err=%v", b.name, n, err)
		}
		bob, err := orm.SelectById[User](ctx, db, users[1].ID)
		if err != nil || bob.City != "sz" || bob.Age != 40 || bob.Name != "bob" {
			t.Fatalf("[%s] UpdateByIdsSets 后 bob 不符：city=%q age=%d name=%q err=%v",
				b.name, bob.City, bob.Age, bob.Name, err)
		}
		alice, err := orm.SelectById[User](ctx, db, users[0].ID)
		if err != nil || alice.City != "bj" {
			t.Fatalf("[%s] 未列入的 alice 不应被改：city=%q err=%v", b.name, alice.City, err)
		}

		// ---- DeleteByIds：软删 2 行（参数顺序的真正考验）----
		n, err = orm.DeleteByIds[User](ctx, db, users[0].ID, users[1].ID)
		if err != nil || n != 2 {
			t.Fatalf("[%s] DeleteByIds：n=%d err=%v —— 若 n=0 则是 SET/IN 参数错位（静默不删）",
				b.name, n, err)
		}
		left, err := orm.SelectList[User](ctx, db, orm.NewQuery[User]())
		if err != nil {
			t.Fatalf("[%s] SelectList 出错：%v", b.name, err)
		}
		if len(left) != 2 {
			t.Fatalf("[%s] 软删 2 行后可见应剩 2 行，实际 %d", b.name, len(left))
		}
		// 被软删的行物理上还在，只是 deleted_at 非空
		idCol := orm.Col[User](func(u *User) *int64 { return &u.ID })
		deleted, err := orm.SelectList[User](ctx, db,
			orm.NewQuery[User]().Unscoped().In(idCol, []any{users[0].ID, users[1].ID}))
		if err != nil || len(deleted) != 2 {
			t.Fatalf("[%s] Unscoped 应取回 2 行软删记录，got %d err=%v", b.name, len(deleted), err)
		}
		for _, u := range deleted {
			if u.DeletedAt == nil {
				t.Fatalf("[%s] id=%d 应有非空 deleted_at", b.name, u.ID)
			}
		}

		// ---- ForceDeleteByIds：无视软删物理删除 ----
		n, err = orm.ForceDeleteByIds[User](ctx, db, users[0].ID)
		if err != nil || n != 1 {
			t.Fatalf("[%s] ForceDeleteByIds：n=%d err=%v", b.name, n, err)
		}
		physically, err := orm.SelectList[User](ctx, db,
			orm.NewQuery[User]().Unscoped().Eq(idCol, users[0].ID))
		if err != nil || len(physically) != 0 {
			t.Fatalf("[%s] 物理删除后 Unscoped 也不应查到，got %d err=%v", b.name, len(physically), err)
		}

		// ---- Save ----
		// 主键零值 → 插入并回填自增主键
		nu := User{Name: "erin", Age: 28, Score: 1, City: "bj", CreatedAt: time.Now()}
		if err := orm.Save[User](ctx, db, &nu); err != nil {
			t.Fatalf("[%s] Save(插入) 出错：%v", b.name, err)
		}
		if nu.ID == 0 {
			t.Fatalf("[%s] Save(插入) 应回填自增主键", b.name)
		}
		// 主键非零且存在 → 全列更新；原字段值随实体写回，不应丢列
		nu.Age = 99
		if err := orm.Save[User](ctx, db, &nu); err != nil {
			t.Fatalf("[%s] Save(更新) 出错：%v", b.name, err)
		}
		back, err := orm.SelectById[User](ctx, db, nu.ID)
		if err != nil || back.Age != 99 {
			t.Fatalf("[%s] Save(更新) 未生效：age=%d err=%v", b.name, back.Age, err)
		}
		if back.Name != "erin" || back.City != "bj" {
			t.Fatalf("[%s] Save(更新) 不应丢字段：name=%q city=%q", b.name, back.Name, back.City)
		}
		// 主键非零但记录不存在 → ErrNotFound（不静默、不退回 Insert）
		ghost := User{ID: 424242, Name: "ghost", CreatedAt: time.Now()}
		err = orm.Save[User](ctx, db, &ghost)
		if !errors.Is(err, orm.ErrNotFound) {
			t.Fatalf("[%s] Save(不存在的主键) 应 ErrNotFound，实际 %v", b.name, err)
		}
	})
}

// TestBatchByIDSoftDeleteArgOrderOnRealDB 专测软删批量删除的参数顺序：
// 插 4 行删 3 行剩 1，行数对不上即参数错位（mock 层对位置型 ? 不可分辨，
// 本用例与 TestBatchByIDApisOnRealDB 的软删段是这一回归的唯一 gate）。
func TestBatchByIDSoftDeleteArgOrderOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db)
		n, err := orm.DeleteByIds[User](ctx, db, users[0].ID, users[1].ID, users[2].ID)
		if err != nil || n != 3 {
			t.Fatalf("[%s] DeleteByIds(3 行)：n=%d err=%v", b.name, n, err)
		}
		left, err := orm.Count[User](ctx, db, orm.NewQuery[User]())
		if err != nil || left != 1 {
			t.Fatalf("[%s] 软删 3 行后可见应剩 1 行，实际 %d err=%v", b.name, left, err)
		}
	})
}

// TestDescribeTableOnRealDB AutoMigrate 之后自省结果应与模型一致。
// 断言只挑跨方言稳定的字段：id 主键 + 自增（PG 的 BIGSERIAL 走 nextval 默认值
// 判定、MySQL 走 auto_increment 标记、SQLite 按 INTEGER 主键推断）、
// deleted_at 可空。Nullable 的其余形态各方言本就不同（如 SQLite 的
// INTEGER PRIMARY KEY 在 pragma 里 notnull=0），不做跨方言断言。
func TestDescribeTableOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		cols, err := orm.DescribeTable(ctx, db, "it_users")
		if err != nil {
			t.Fatalf("[%s] DescribeTable 出错：%v", b.name, err)
		}
		byName := map[string]orm.ColumnMeta{}
		for _, c := range cols {
			byName[c.Name] = c
		}
		for _, want := range []string{"id", "name", "age", "score", "city", "meta", "created_at", "deleted_at"} {
			if _, ok := byName[want]; !ok {
				t.Fatalf("[%s] 缺少列 %q（got %d 列：%v）", b.name, want, len(cols), colNames(cols))
			}
		}
		id := byName["id"]
		if !id.PrimaryKey || !id.AutoIncrement {
			t.Fatalf("[%s] id 列应为主键 + 自增：%+v", b.name, id)
		}
		if del := byName["deleted_at"]; !del.Nullable {
			t.Fatalf("[%s] deleted_at 列应可空：%+v", b.name, del)
		}
		// TableExists / ColumnExists 与 DescribeTable 同源
		ok, err := orm.TableExists(ctx, db, "it_users")
		if err != nil || !ok {
			t.Fatalf("[%s] TableExists(it_users) 应 true，got %v err=%v", b.name, ok, err)
		}
		ok, err = orm.TableExists(ctx, db, "it_missing_table")
		if err != nil || ok {
			t.Fatalf("[%s] TableExists(不存在的表) 应 false，got %v err=%v", b.name, ok, err)
		}
		ok, err = orm.ColumnExists(ctx, db, "it_users", "Created_At") // 大小写归一
		if err != nil || !ok {
			t.Fatalf("[%s] ColumnExists 大小写归一应命中，got %v err=%v", b.name, ok, err)
		}
	})
}

func colNames(cols []orm.ColumnMeta) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

// TestUpdateByIdsOnRealDB —— 逐条更新真的落到每一行，且未列入的行不受影响。
//
// 为什么必须真库：mock 只能数出「执行了两条 UPDATE」，证明不了 WHERE 的实参绑到了
// 对应的主键 —— 位置型方言上实参错位仍是一条语法正确的 SQL，只会静默改错行。
func TestUpdateByIdsOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db)

		users[0].Name, users[0].Age = "alice-v2", 31
		users[1].Name, users[1].Age = "bob-v2", 26
		if err := orm.UpdateByIds[User](ctx, db, users[:2]); err != nil {
			t.Fatalf("[%s] UpdateByIds 出错：%v", b.name, err)
		}

		got, err := orm.SelectByIds[User](ctx, db, users[0].ID, users[1].ID)
		if err != nil {
			t.Fatalf("[%s] 回读失败：%v", b.name, err)
		}
		byID := make(map[int64]User, len(got))
		for _, u := range got {
			byID[u.ID] = u
		}
		if u := byID[users[0].ID]; u.Name != "alice-v2" || u.Age != 31 {
			t.Fatalf("[%s] 第 0 行未按预期更新：%+v", b.name, u)
		}
		if u := byID[users[1].ID]; u.Name != "bob-v2" || u.Age != 26 {
			t.Fatalf("[%s] 第 1 行未按预期更新：%+v", b.name, u)
		}

		// 未列入批量列表的行必须原样保留（这是「逐条按主键」而不是「全表更新」的证据）。
		third, err := orm.SelectById[User](ctx, db, users[2].ID)
		if err != nil {
			t.Fatalf("[%s] 回读第 2 行失败：%v", b.name, err)
		}
		if third.Name != "carol" || third.Age != 35 {
			t.Fatalf("[%s] 未列入的行被误改：%+v", b.name, third)
		}

		// 零主键：批量更新不做插入，必须报错且指到具体行号。
		if err := orm.UpdateByIds[User](ctx, db, []User{{ID: 0, Name: "ghost"}}); err == nil {
			t.Fatalf("[%s] 零主键应报错，实际通过", b.name)
		} else if !strings.Contains(err.Error(), "第 0 行") {
			t.Fatalf("[%s] 错误应带行号，实际：%v", b.name, err)
		}
	})
}

// TestUpdateByIdsOptimisticLockOnRealDB —— 批量入口的乐观锁与单行版同一口径：
// 任一行版本冲突就中断，且错误可被 errors.Is 判到 ErrOptimisticLock。
func TestUpdateByIdsOptimisticLockOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		if err := db.AutoMigrate(ctx, &Doc{}); err != nil {
			t.Fatalf("[%s] AutoMigrate 失败：%v", b.name, err)
		}
		docs := []Doc{{Title: "a"}, {Title: "b"}}
		for i := range docs {
			if err := orm.Insert(ctx, db, &docs[i]); err != nil {
				t.Fatalf("[%s] Insert 第 %d 行失败：%v", b.name, i, err)
			}
		}

		// 先正常更新一次把库里的 version 推高（实体也回写成新值），再把内存版本
		// 人为退回一档 —— 等价于「别处已经改过这一行」。版本基数由真库读回，
		// 不假设插入时的默认值（各后端对非空整型列的初始值处理一致，但不必依赖它）。
		docs[0].Title = "a-touched"
		if err := orm.UpdateById(ctx, db, &docs[0]); err != nil {
			t.Fatalf("[%s] 预更新失败：%v", b.name, err)
		}
		cur, err := orm.SelectById[Doc](ctx, db, docs[0].ID)
		if err != nil {
			t.Fatalf("[%s] 回读第 0 行失败：%v", b.name, err)
		}
		docs[0].Version = cur.Version - 1
		docs[0].Title, docs[1].Title = "a2", "b2"

		err = orm.UpdateByIds[Doc](ctx, db, docs)
		if err == nil {
			t.Fatalf("[%s] 第 0 行版本已过期，应报冲突，实际通过", b.name)
		}
		if !errors.Is(err, orm.ErrOptimisticLock) {
			t.Fatalf("[%s] 应为 ErrOptimisticLock，实际：%v", b.name, err)
		}
		if !strings.Contains(err.Error(), "第 0 行") {
			t.Fatalf("[%s] 错误应指出冲突发生在第 0 行：%v", b.name, err)
		}
		// 第 1 行没有执行 —— 遇错即停。
		second, err := orm.SelectById[Doc](ctx, db, docs[1].ID)
		if err != nil {
			t.Fatalf("[%s] 回读第 1 行失败：%v", b.name, err)
		}
		if second.Title != "b" {
			t.Fatalf("[%s] 遇错即停：第 1 行不应被更新，实际 title=%q", b.name, second.Title)
		}
	})
}
