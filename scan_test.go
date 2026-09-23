package orm

import (
	"context"
	"database/sql/driver"
	"fmt"
	"sync"
	"testing"
)

// 行映射计划是**跨查询共享**的缓存（挂在模型元数据上），所以这里专门盯两件事：
// 1) 不同结果集列集合必须各自拿到正确的计划（含列序变化、列子集、未知列）；
// 2) 并发查询下的正确性（本机无 gcc，跑不了 -race，至少保证不出错乱与 panic）。

// TestScanPlanColumnSets 同一个模型先后用不同列集合查询，映射都必须正确。
// 这条是关键回归：计划按「列签名」缓存，一旦签名判断或列→字段解析写错，
// 表现就是「第二次查询映射到错误的字段」，而这种错误在单次查询的用例里看不出来。
func TestScanPlanColumnSets(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		cols []string
		data []driver.Value
	}{
		{"全列", []string{"id", "name", "age"}, []driver.Value{int64(7), "alice", int64(30)}},
		{"列序颠倒", []string{"age", "name", "id"}, []driver.Value{int64(31), "bob", int64(8)}},
		{"列子集", []string{"name"}, []driver.Value{"carol"}},
		{"含未知列", []string{"id", "unmapped_col", "age"}, []driver.Value{int64(9), "zzz", int64(33)}},
		{"结果集为空", []string{"id", "name", "age"}, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mockRegistry["users"] = &mockRows{
				cols: c.cols,
				data: func() [][]driver.Value {
					if c.data == nil {
						return nil
					}
					return [][]driver.Value{c.data}
				}(),
			}
			db := newMockDB(t)

			// 走 SelectList（而不是 SelectById）：列表路径才用 newRowScanner 复用计划，
			// 也正是被改动的路径。
			list, err := SelectList(ctx, db, NewQuery[User]())
			if err != nil {
				t.Fatalf("SelectList 失败：%v", err)
			}
			if c.data == nil {
				if len(list) != 0 {
					t.Fatalf("空结果集应返回 0 行，实际 %d", len(list))
				}
				return
			}
			if len(list) != 1 {
				t.Fatalf("应返回 1 行，实际 %d", len(list))
			}
			want := expectedUser(c.cols, c.data)
			if list[0] != want {
				t.Fatalf("映射结果错误：实际 %+v，期望 %+v", list[0], want)
			}
		})
	}
}

// expectedUser 按「列名 → 字段」的期望语义还原目标结构体。
// 未被结果集覆盖的字段保持零值。
func expectedUser(cols []string, data []driver.Value) User {
	var u User
	for i, c := range cols {
		switch c {
		case "id":
			u.Id = data[i].(int64)
		case "name":
			u.Name = data[i].(string)
		case "age":
			u.Age = int(data[i].(int64))
		}
	}
	return u
}

// TestScanPlanConcurrent 多个 goroutine 同时用同一个模型查询：
// 计划缓存是共享的，并发构建/命中都必须安全。
func TestScanPlanConcurrent(t *testing.T) {
	ctx := context.Background()
	mockFactories["users"] = func() driver.Rows {
		return &mockRows{
			cols: []string{"id", "name", "age"},
			data: [][]driver.Value{{int64(5), "concurrent", int64(20)}},
		}
	}
	defer delete(mockFactories, "users")

	db := newMockDB(t)

	const (
		workers = 8
		rounds  = 200
	)
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				list, err := SelectList(ctx, db, NewQuery[User]().Limit(1))
				if err != nil {
					errCh <- err
					return
				}
				if len(list) != 1 || list[0].Id != 5 || list[0].Name != "concurrent" || list[0].Age != 20 {
					errCh <- fmt.Errorf("期望 1 行 {Id:5 Name:concurrent Age:20}，实际 %+v", list)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发查询结果错误：%v", err)
	}
}

// TestScanPlanCacheCap 越过计划缓存上限后，未缓存的计划仍必须映射正确
// （上限之外的退路是「每次查询重建一次计划」，不是「算错」）。
func TestScanPlanCacheCap(t *testing.T) {
	ctx := context.Background()

	// 9 列 → 2^9-1 = 511 种非空列组合，足以越过上限。
	all := scanBenchUserCols
	if 1<<len(all) <= maxScanPlansPerModel {
		t.Fatalf("%d 列只能产生 %d 种组合，覆盖不到上限 %d", len(all), 1<<len(all), maxScanPlansPerModel)
	}

	var current []string
	var want scanBenchUser
	mockFactories["scan_bench_users"] = func() driver.Rows {
		want = scanBenchUser{}
		data := make([]driver.Value, len(current))
		for i, c := range current {
			data[i] = capColValue(c)
			capApply(&want, c)
		}
		return &mockRows{cols: append([]string(nil), current...), data: [][]driver.Value{data}}
	}
	defer delete(mockFactories, "scan_bench_users")

	db := newMockDB(t)
	queried := 0
	for mask := 1; mask < 1<<len(all); mask++ {
		current = current[:0]
		for i, c := range all {
			if mask&(1<<i) != 0 {
				current = append(current, c)
			}
		}
		got, err := SelectList(ctx, db, NewQuery[scanBenchUser]())
		if err != nil {
			t.Fatalf("mask=%09b 列=%v 查询失败：%v", mask, current, err)
		}
		if len(got) != 1 {
			t.Fatalf("mask=%09b 列=%v 应返回 1 行，实际 %d", mask, current, len(got))
		}
		if got[0] != want {
			t.Fatalf("mask=%09b 列=%v 映射错误：\n实际 %+v\n期望 %+v", mask, current, got[0], want)
		}
		queried++
	}
	if queried <= maxScanPlansPerModel {
		t.Fatalf("只覆盖了 %d 种列组合，未能越过上限 %d", queried, maxScanPlansPerModel)
	}
}

// capColValue / capApply 是同一份取值定义的两种表达：前者给驱动，后者给期望值。
func capColValue(col string) driver.Value {
	switch col {
	case "id", "age":
		return int64(22)
	case "name", "city", "email":
		return "v"
	case "score":
		return float64(3.5)
	case "created_at", "updated_at":
		return scanBenchTime
	}
	return nil // deleted_at
}

func capApply(u *scanBenchUser, col string) {
	switch col {
	case "id":
		u.ID = 22
	case "age":
		u.Age = 22
	case "name":
		u.Name = "v"
	case "city":
		u.City = "v"
	case "email":
		u.Email = "v"
	case "score":
		u.Score = 3.5
	case "created_at":
		u.CreatedAt = scanBenchTime
	case "updated_at":
		u.UpdatedAt = scanBenchTime
	}
}
