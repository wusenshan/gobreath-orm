package orm

import "errors"

// ErrNotFound 查询单条记录但未命中时返回。
var ErrNotFound = errors.New("orm: 记录不存在")
