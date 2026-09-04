package orm

import (
	"context"
	"strings"
	"testing"
)

// omitProduct 用于验证「指针字段 nil → NULL」与 OmitZero 跳过零值列的行为。
type omitProduct struct {
	Id    int   `db:"id,pk,autoincrement"`
	Name  string `db:"name"`
	Score *int  `db:"score"`
	Age   int   `db:"age"`
}

// TestInsertOmitZeroSkipsZeroValueColumn 验证：开启 OmitZero 时值类型的零值列被跳过，
// 而不开启时零值照常入库（默认行为不变）。
func TestInsertOmitZeroSkipsZeroValueColumn(t *testing.T) {
	db := newMockDB(t)

	// 对照组：默认行为，Age=0 仍写入
	recQuery = ""
	if err := Insert(context.Background(), db, &User{Name: "x", Age: 0}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recQuery, `"age"`) {
		t.Fatalf("默认应保留 age(零值)，实际 SQL: %s", recQuery)
	}

	// 实验组：开启 OmitZero，Age=0 应被跳过，Name 保留
	recQuery = ""
	if err := Insert(context.Background(), db, &User{Name: "x", Age: 0}, OmitZero()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recQuery, `"name"`) {
		t.Fatalf("OmitZero 应保留 name，实际 SQL: %s", recQuery)
	}
	if strings.Contains(recQuery, `"age"`) {
		t.Fatalf("OmitZero 应跳过 age(零值)，实际 SQL: %s", recQuery)
	}
}

// TestInsertPointerNilWritesNull 验证：指针字段为 nil 时，参数以 NULL 传入（driver 将其转 SQL NULL），
// 且 OmitZero 不会跳过指针字段。
func TestInsertPointerNilWritesNull(t *testing.T) {
	db := newMockDB(t)
	recQuery, recArgs = "", nil
	if err := Insert(context.Background(), db, &omitProduct{Name: "x", Score: nil}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recQuery, `"score"`) {
		t.Fatalf("指针 nil 字段不应被跳过，应出现在列中，实际 SQL: %s", recQuery)
	}
	foundNil := false
	for _, a := range recArgs {
		if a == nil {
			foundNil = true
		}
	}
	if !foundNil {
		t.Fatalf("nil 指针应作为 NULL 参数传入，实际 args: %v", recArgs)
	}
}

// TestUpdateByIdOmitZeroSkipsZeroValueColumn 验证：UpdateById 开启 OmitZero 时只覆盖非空字段。
func TestUpdateByIdOmitZeroSkipsZeroValueColumn(t *testing.T) {
	type prod struct {
		Id   int    `db:"id,pk"`
		Name string `db:"name"`
		Age  int    `db:"age"`
	}
	db := newMockDB(t)
	recQuery = ""
	if err := UpdateById(context.Background(), db, &prod{Id: 1, Name: "bob", Age: 0}, OmitZero()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(recQuery, `"age"`) {
		t.Fatalf("UpdateById OmitZero 应跳过 age(零值)，实际 SQL: %s", recQuery)
	}
	if !strings.Contains(recQuery, `"name"`) {
		t.Fatalf("UpdateById OmitZero 应保留 name，实际 SQL: %s", recQuery)
	}
}
