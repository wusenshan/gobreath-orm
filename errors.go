package orm

import "errors"

// ErrNotFound 查询单条记录但未命中时返回。
var ErrNotFound = errors.New("orm: 记录不存在")

// ErrInvalidArgument 调用方传入的参数不满足前置条件时返回，调用方可用
// errors.Is 区分后直接提示参数错误（无需重试）。
//
// 当前产生的场景（随版本可能扩充，判别一律走 errors.Is）：
//   - 批量按主键 API（SelectByIds / DeleteByIds / ForceDeleteByIds / UpdateByIdsSets）
//     传入空 ID 列表 —— `IN ()` 不是合法 SQL，若折叠成恒真条件则等价于
//     查询/更新/删除全表，这类调用几乎总是漏传参数的笔误，必须显式失败；
//   - Save 传入 nil 实体。
var ErrInvalidArgument = errors.New("orm: 参数不合法")

// ErrOptimisticLock 乐观锁冲突时返回：期望的版本与数据库当前版本不一致
// （记录已被其他事务修改），更新未生效，调用方应重试或提示用户。
var ErrOptimisticLock = errors.New("orm: 乐观锁冲突，记录已被其他事务修改")
