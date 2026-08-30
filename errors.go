package orm

import "errors"

// ErrNotFound 查询单条记录但未命中时返回。
var ErrNotFound = errors.New("orm: 记录不存在")

// ErrOptimisticLock 乐观锁冲突时返回：期望的版本与数据库当前版本不一致
// （记录已被其他事务修改），更新未生效，调用方应重试或提示用户。
var ErrOptimisticLock = errors.New("orm: 乐观锁冲突，记录已被其他事务修改")
