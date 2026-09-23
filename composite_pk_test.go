package orm

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// ---- 复合主键闸门 ----
//
// 背景：parseMeta 原先是 `m.pk = &m.fields[len(m.fields)-1]`，多个 ,pk 列时**后者静默覆盖前者**。
// 模型照样能构造，但主键只认最后一列，于是 DeleteById / UpdateById / Upsert 生成的 WHERE 少一半
// 条件，可能命中并改写多行；AutoMigrate 还会产出两个内联 PRIMARY KEY 的非法 DDL
// （MySQL 1068 / PG "multiple primary keys"）。现在改为解析期硬失败，这组用例锁住该行为。

// TestParseMetaCompositePKPanics 两个 ,pk 列必须在解析期 panic，且诊断信息点名涉及的列。
func TestParseMetaCompositePKPanics(t *testing.T) {
	type composite struct {
		TenantId int64  `db:"tenant_id,pk"`
		UserId   int64  `db:"user_id,pk"`
		Name     string `db:"name"`
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("声明两个 ,pk 列时应 panic，实际没有 —— 主键会被静默截断为最后一列")
		}
		msg := fmt.Sprint(r)
		for _, want := range []string{"tenant_id", "user_id", "复合主键"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("panic 信息应包含 %q，实际: %s", want, msg)
			}
		}
	}()
	parseMeta(reflect.TypeOf(composite{}))
}

// TestParseMetaCompositePKMixedAutoInc 显式 ,pk 与 ,pk,autoincrement 混用同样要拦住
// （不能只在「两个都不带自增」时才报错）。
func TestParseMetaCompositePKMixedAutoInc(t *testing.T) {
	type mixed struct {
		TenantId int64 `db:"tenant_id,pk"`
		UserId   int64 `db:"user_id,pk,autoincrement"`
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("显式主键 + 自增主键的复合声明同样应 panic，实际没有")
		}
	}()
	parseMeta(reflect.TypeOf(mixed{}))
}

// TestParseMetaSinglePKUnaffected 单列主键路径行为不变。
func TestParseMetaSinglePKUnaffected(t *testing.T) {
	type single struct {
		Id   int64  `db:"id,pk,autoincrement"`
		Name string `db:"name"`
	}
	m := parseMeta(reflect.TypeOf(single{}))
	if m.pk == nil || m.pk.colName != "id" {
		t.Fatalf("pk 应指向 id 列，实际 %+v", m.pk)
	}
	if !m.pk.autoInc {
		t.Fatal("id 列应保留 autoincrement 标记")
	}
	// 主键指针必须直接指向 m.fields 的元素：旧实现是循环内 `&m.fields[len-1]`，
	// 一旦后续 append 触发扩容就会指向**旧底层数组的副本**，从此与 m.fields 脱钩。
	if m.pk != &m.fields[0] {
		t.Fatal("pk 应指向 m.fields[0] 本身，而不是它的副本")
	}
}

// TestParseMetaInferredPK 「无 ,pk 时按字段名 ID 推断」这条路径同样要填好 pk。
func TestParseMetaInferredPK(t *testing.T) {
	type inferred struct {
		Id   int64
		Name string
	}
	m := parseMeta(reflect.TypeOf(inferred{}))
	if m.pk == nil || m.pk.colName != "id" {
		t.Fatalf("推断路径应填好 pk，实际 %+v", m.pk)
	}
	if !m.fields[0].pk {
		t.Fatal("被推断为主键的字段应置 pk=true")
	}
	if m.pk != &m.fields[0] {
		t.Fatal("推断出的主键指针也应指向 m.fields 的元素本身")
	}
}

// TestParseMetaNoPK 无主键模型不得误填 pk（DeleteById 等要据此报错）。
func TestParseMetaNoPK(t *testing.T) {
	type noPK struct {
		Name string `db:"name"`
		Age  int    `db:"age"`
	}
	m := parseMeta(reflect.TypeOf(noPK{}))
	if m.pk != nil {
		t.Fatalf("无主键模型应 pk=nil，实际 %+v", m.pk)
	}
}
