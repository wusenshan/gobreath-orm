package orm

import (
	"context"
	"strings"
	"testing"
)

// Preload 的三道防线：外键列拼错、has_many 字段非切片、父切片含 nil 指针。
// 此前这三处都是「静默做错事」或「反射 panic」，现在统一翻译成带模型/关系名的 error。

type pgChild struct {
	Id     int64  `db:"id,pk,autoincrement"`
	UserId int64  `db:"user_id"`
	Name   string `db:"name"`
}

type pgParent struct {
	Id      int64     `db:"id,pk,autoincrement"`
	Name    string    `db:"name"`
	Childs  []pgChild `db:"-" orm:"has_many"`
	Profile *pgChild  `db:"-" orm:"has_one;fk:user_id"`
	// BadProf 故意把外键列拼成不存在的 child_id
	BadProf *pgChild `db:"-" orm:"belongs_to;fk:child_id"`
}

func (pgParent) TableName() string { return "pg_parents" }
func (pgChild) TableName() string  { return "pg_children" }

func TestPreloadRejectsBadBelongsToFK(t *testing.T) {
	db := newMockDB(t)
	parents := []pgParent{{Id: 1, Name: "a"}}
	err := Preload(context.Background(), db, &parents, "BadProf")
	if err == nil {
		t.Fatal("外键列拼错应返回 error，实际 nil（静默全空）")
	}
	if !strings.Contains(err.Error(), "child_id") || !strings.Contains(err.Error(), "pg_parents") {
		t.Fatalf("错误信息应指明列名与模型名，实际：%v", err)
	}
}

// pgBadManyParent 故意把 has_many 关系标在非切片字段上。
type pgBadManyParent struct {
	Id     int64   `db:"id,pk,autoincrement"`
	Childs pgChild `db:"-" orm:"has_many"` // 应为切片，写成单值
}

func (pgBadManyParent) TableName() string { return "pg_bad_many" }

func TestPreloadRejectsHasManyOnNonSliceField(t *testing.T) {
	db := newMockDB(t)
	bp := []pgBadManyParent{{Id: 1}}
	err := Preload(context.Background(), db, &bp, "Childs")
	if err == nil {
		t.Fatal("has_many 标在非切片字段上应返回 error，实际 nil（之后会 reflect panic）")
	}
	if !strings.Contains(err.Error(), "has_many") || !strings.Contains(err.Error(), "不是切片") {
		t.Fatalf("错误信息应指明关系种类与类型问题，实际：%v", err)
	}
}

func TestPreloadSkipsNilPointerParents(t *testing.T) {
	db := newMockDB(t)
	// 父切片含一个 nil 指针：此前 derefValue 返回 nil ptr，
	// 之后 FieldByName 对 nil ptr Value 直接 panic。
	parents := []*pgParent{nil, {Id: 1, Name: "a"}}
	// 不应 panic；nil 元素被跳过，第二个元素正常处理（查不到子行不报错）。
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("nil 指针父元素不应 panic，实际：%v", r)
			}
		}()
		err := Preload(context.Background(), db, &parents, "Profile")
		if err != nil {
			t.Fatalf("跳过 nil 指针后不应报错，实际：%v", err)
		}
	}()
}

// has_one/belongs_to 标在切片字段上也该拦。
type pgBadOneParent struct {
	Id    int64     `db:"id,pk,autoincrement"`
	Profs []pgChild `db:"-" orm:"has_one"` // 应为单值，写成切片
}

func (pgBadOneParent) TableName() string { return "pg_bad_one" }

func TestPreloadRejectsHasOneOnSliceField(t *testing.T) {
	db := newMockDB(t)
	bp := []pgBadOneParent{{Id: 1}}
	err := Preload(context.Background(), db, &bp, "Profs")
	if err == nil {
		t.Fatal("has_one 标在切片字段上应返回 error")
	}
}
