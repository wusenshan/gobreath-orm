package orm

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// Save 的「命中 0 行」契约按模型是否带乐观锁版本列分叉 —— 这两条用例把它钉住。
//
// 背景（v0.1.15）：updateByIdImpl 在带版本列时把 0 行一律报成 ErrOptimisticLock
// （它分不清「记录不存在」和「版本冲突」），于是 Save 注释里承诺的 ErrNotFound
// 在这类模型上**永远不可达**，调用方 errors.Is(err, ErrNotFound) 判「不存在」会漏判。

func TestSaveZeroRowsNoVersionIsNotFound(t *testing.T) {
	uibReset()
	atomic.StoreInt64(&uibAffected, 0)
	err := Save[User](context.Background(), uibDB(t), &User{Id: 1, Name: "x"})
	if err == nil {
		t.Fatal("命中 0 行应报错")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("无版本列时应报 ErrNotFound，实际: %v", err)
	}
	if errors.Is(err, ErrOptimisticLock) {
		t.Fatalf("无版本列时不该报乐观锁冲突，实际: %v", err)
	}
}

func TestSaveZeroRowsWithVersionIsOptimisticLock(t *testing.T) {
	uibReset()
	atomic.StoreInt64(&uibAffected, 0)
	err := Save[batchVerUser](context.Background(), uibDB(t),
		&batchVerUser{Id: 1, Name: "x", Version: 2})
	if err == nil {
		t.Fatal("命中 0 行应报错")
	}
	if !errors.Is(err, ErrOptimisticLock) {
		t.Fatalf("带版本列时应报 ErrOptimisticLock，实际: %v", err)
	}
	// 不能再同时满足 ErrNotFound —— 那会让「版本冲突」被误判成「记录不存在」。
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("带版本列时不应报 ErrNotFound，实际: %v", err)
	}
	// 文案必须点明三种可能并给出确认办法，否则调用方会当成纯并发冲突无脑重试。
	for _, want := range []string{"记录不存在", "已被逻辑删除", "版本冲突", "SelectById"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误文案应包含 %q，实际: %v", want, err)
		}
	}
}
