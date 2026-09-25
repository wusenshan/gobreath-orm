package orm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// jsonUpdRow：JSON 列 + 无软删除列的极简模型，专测部分更新路径的绑定口径。
type jsonUpdRow struct {
	Id   int64          `db:"id,pk,autoincrement"`
	Name string         `db:"name"`
	Meta map[string]any `db:"meta,json"`
}

var (
	jsonUpdName = Col[jsonUpdRow](func(r *jsonUpdRow) *string { return &r.Name })
	jsonUpdMeta = Col[jsonUpdRow](func(r *jsonUpdRow) *map[string]any { return &r.Meta })
)

// UpdatePartial 此前直接 q.sets = sets，把 map 写进调用方的 Query：
//   - 之后在同一 q 上链 .Set() 会**追加到上一次的 map 里**（Set 只在 map 为 nil 时新建），
//     残留字段混进下一次 UpdateSets，静默多更新一列（数据损坏级）；
//   - 调用方持有的 map 与 q.sets 从此共享底层存储。
//
// 契约：与查询入口一致 —— 框架内部动作不得修改调用方传入的 Query。
func TestUpdatePartialDoesNotMutateCallerQuery(t *testing.T) {
	db := newMockDB(t)
	ctx := context.Background()

	q := NewQuery[jsonUpdRow]().Eq(jsonUpdName, "a")
	if _, err := UpdatePartial[jsonUpdRow](ctx, db, q, map[string]any{"name": "b"}); err != nil {
		t.Fatalf("UpdatePartial 失败：%v", err)
	}
	if len(q.sets) != 0 {
		t.Fatalf("UpdatePartial 把 sets 写回了调用方的 Query（残留 %v）；"+
			"之后在同一 q 上链 .Set() 会追加到这份旧 map，残留字段会混进下一次 UPDATE", q.sets)
	}

	// 用户可见后果：UpdatePartial 之后复用 q 链 Set 再 UpdateSets，SQL 里不得出现上一次的字段。
	q2 := NewQuery[jsonUpdRow]().Eq(jsonUpdName, "a")
	if _, err := UpdatePartial[jsonUpdRow](ctx, db, q2, map[string]any{"meta": map[string]any{"x": 1}}); err != nil {
		t.Fatalf("UpdatePartial 失败：%v", err)
	}
	q2.Set(jsonUpdName, "bob")
	recQuery = ""
	if _, err := UpdateSets[jsonUpdRow](ctx, db, q2); err != nil {
		t.Fatalf("UpdateSets 失败：%v", err)
	}
	if !strings.Contains(recQuery, `"name"`) {
		t.Fatalf("前置条件不成立：第二次 UPDATE 未包含本次 Set 的列，用例无意义：%s", recQuery)
	}
	if strings.Contains(recQuery, `"meta"`) {
		t.Fatalf("第一次 UpdatePartial 的 sets 残留在调用方 q 上，第二次 UPDATE 静默多更新了 meta 列：%s", recQuery)
	}
}

// UpdateSets / UpdateByIdSets 的 map 值此前经 bindVal 绑定，而 bindVal 只处理了向量列、
// 漏了 JSON 列 —— 实体路径（Insert/Update 走 argFor）会先 json.Marshal，map 路径却把
// map[string]any 原样交给驱动（database/sql 报 unsupported type）。
// 契约：同一个值，走实体更新与 map 更新必须绑定出相同的参数。
func TestUpdateSetsMarshalsJSONColumns(t *testing.T) {
	db := newMockDB(t)
	ctx := context.Background()

	meta := map[string]any{"a": 1}
	want, _ := json.Marshal(meta)

	t.Run("UpdateSets", func(t *testing.T) {
		q := NewQuery[jsonUpdRow]().Eq(jsonUpdName, "a").Set(jsonUpdMeta, meta)
		recArgs = nil
		if _, err := UpdateSets[jsonUpdRow](ctx, db, q); err != nil {
			t.Fatalf("UpdateSets 失败：%v", err)
		}
		b, ok := recArgs[0].([]byte)
		if !ok {
			t.Fatalf("JSON 列经 UpdateSets 绑定的是 %T（期望 marshal 后的 []byte），驱动会拒绝：%v", recArgs[0], recArgs[0])
		}
		if string(b) != string(want) {
			t.Fatalf("JSON 列绑定值 = %s；期望 %s（与实体路径 argFor 同口径）", b, want)
		}
	})

	t.Run("UpdateByIdSets", func(t *testing.T) {
		recArgs = nil
		if _, err := UpdateByIdSets[jsonUpdRow](ctx, db, 1, map[string]any{"meta": meta}); err != nil {
			t.Fatalf("UpdateByIdSets 失败：%v", err)
		}
		b, ok := recArgs[0].([]byte)
		if !ok {
			t.Fatalf("JSON 列经 UpdateByIdSets 绑定的是 %T（期望 marshal 后的 []byte）：%v", recArgs[0], recArgs[0])
		}
		if string(b) != string(want) {
			t.Fatalf("JSON 列绑定值 = %s；期望 %s", b, want)
		}
	})
}

// Table() 的文档写明用途含「分表」，但按条件写入的四个入口此前一律用
// meta.finalTable(db.prefix) 拼表名 —— q.Table("users_2024") 被静默忽略，
// UPDATE / DELETE 打到模型默认表上（读路径 Select 走 Build() 是尊重 q.table 的）。
// 契约：凡接收 *Query[T] 的入口，目标表必须以查询构造器为准。
func TestConditionalWritesHonorQueryTable(t *testing.T) {
	db := newMockDB(t)
	ctx := context.Background()

	entries := []struct {
		name string
		run  func(t *testing.T, q *Query[jsonUpdRow]) error
	}{
		{"Update", func(t *testing.T, q *Query[jsonUpdRow]) error {
			return Update[jsonUpdRow](ctx, db, q, &jsonUpdRow{Name: "x"})
		}},
		{"UpdateSets", func(t *testing.T, q *Query[jsonUpdRow]) error {
			_, err := UpdateSets[jsonUpdRow](ctx, db, q.Set(jsonUpdName, "x"))
			return err
		}},
		{"Delete", func(t *testing.T, q *Query[jsonUpdRow]) error {
			return Delete[jsonUpdRow](ctx, db, q)
		}},
		{"ForceDelete", func(t *testing.T, q *Query[jsonUpdRow]) error {
			return ForceDelete[jsonUpdRow](ctx, db, q)
		}},
	}

	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			q := NewQuery[jsonUpdRow]().Table("rows_2024").Eq(jsonUpdName, "a")
			recQuery = ""
			if err := e.run(t, q); err != nil {
				t.Fatalf("%s 失败：%v", e.name, err)
			}
			if !strings.Contains(recQuery, "rows_2024") {
				t.Fatalf("前置条件不成立：%s 未使用 q.Table 指定的表，用例无意义：%s", e.name, recQuery)
			}
			if strings.Contains(recQuery, "json_upd_rows") {
				t.Fatalf("%s 忽略了 q.Table(\"rows_2024\")，静默写到了模型默认表：%s", e.name, recQuery)
			}
		})
	}
}
