package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// AutoMigrate 根据传入的实体（指针或零值结构体）自动建表，使数据库结构与 Go 模型保持一致。
// 当前策略是「幂等建表」：生成 CREATE TABLE IF NOT EXISTS（已存在则跳过），并为标记了
// `,index` 的列追加 CREATE INDEX IF NOT EXISTS；不执行删列/改列等破坏性的 ALTER，
// 因此可安全地反复调用而不丢失数据。
//
// 实体可传指针或值：orm.AutoMigrate(ctx, db, &User{}, &Article{})。
// 列类型由 Go 类型按当前方言推导；支持主键/自增、`,vector(N)` 向量列、`,json` 列、
// `,unique` 唯一约束、`,index` 二级索引；向量列还可声明 `,hnsw(cosine)` / `,ivfflat(l2)`
// 让 AutoMigrate 自动建向量索引（PG 生成 USING hnsw/ivfflat + vector_*_ops；MySQL 生成
// CREATE VECTOR INDEX，仅 HeatWave 可用；SQLite 无原生向量索引则跳过）。
//
// 已知限制（v0.1.7）：
//   - 仅做「增量建表」，不自动变更已有列（如改类型、加长度）；改表结构需手动 ALTER。
//   - 复合唯一/复合索引暂不支持，仅单列 `,unique` / `,index`。
//   - 向量维度需通过 `,vector(N)` 显式声明；未声明时默认 1536，建议显式指定以对齐模型维度。
func (db *DB) AutoMigrate(ctx context.Context, models ...any) error {
	for _, m := range models {
		meta, err := metaOfAny(m)
		if err != nil {
			return err
		}
		stmts := migrateStatements(meta, db.dialect, db.prefix)
		for _, s := range stmts {
			if _, err := db.execContext(ctx, s); err != nil {
				return fmt.Errorf("orm: AutoMigrate 执行失败 (%s): %w", s, err)
			}
		}
	}
	return nil
}

// metaOfAny 从任意实体（指针或值）解析模型元数据；无法解析时报错。
func metaOfAny(m any) (*modelMeta, error) {
	v := reflect.ValueOf(m)
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil, fmt.Errorf("orm: AutoMigrate 收到 nil 指针，无法推导表结构")
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("orm: AutoMigrate 仅接受结构体或结构体指针，收到 %T", m)
	}
	return getMetaByType(v.Type()), nil
}

// migrateStatements 返回某模型在当前方言下需要执行的 DDL 语句列表（建表 + 二级索引）。
// 抽取为纯函数便于离线测试断言，AutoMigrate 仅负责依次执行。
func migrateStatements(meta *modelMeta, d Dialect, prefix string) []string {
	table := quoteTable(meta.finalTable(prefix), d)
	var cols []string
	var indexes []string
	for i := range meta.fields {
		f := meta.fields[i]
		if f.ignore {
			continue
		}
		def := fmt.Sprintf("%s %s", d.QuoteIdent(f.colName), columnSQL(f, d))
		if f.unique {
			def += " UNIQUE"
		}
		cols = append(cols, "  "+def)
		if f.index {
			idxName := fmt.Sprintf("idx_%s_%s", strings.ReplaceAll(meta.finalTable(prefix), ".", "_"), f.colName)
			indexes = append(indexes, fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s)",
				d.QuoteIdent(idxName), table, d.QuoteIdent(f.colName)))
		}
		if f.vectorIndexType != "" {
			idxName := fmt.Sprintf("idx_%s_%s", strings.ReplaceAll(meta.finalTable(prefix), ".", "_"), f.colName)
			switch dialectKind(d) {
			case "postgres":
				indexes = append(indexes, fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s USING %s (%s %s)",
					d.QuoteIdent(idxName), table, f.vectorIndexType, d.QuoteIdent(f.colName), vectorOpsClass(f.vectorIndexMetric)))
			case "mysql":
				// MySQL 向量索引语法为 CREATE VECTOR INDEX，无度量参数（度量由查询时的
				// VECTOR_DISTANCE 决定）；且仅 HeatWave on OCI / MySQL AI 支持，社区版执行会报
				// 函数不存在。l1 在 MySQL 整体不支持（VECTOR_DISTANCE 无 MANHATTAN），建索引无意义，跳过。
				if f.vectorIndexMetric != "l1" {
					indexes = append(indexes, fmt.Sprintf("CREATE VECTOR INDEX %s ON %s (%s)",
						d.QuoteIdent(idxName), table, d.QuoteIdent(f.colName)))
				}
			default:
				// SQLite 无原生向量索引，跳过（向量以 TEXT 存储，生产建议用 PG/MySQL）
			}
		}
	}
	// 主键约束：若已在内联列定义（PG SERIAL PRIMARY KEY / MySQL AUTO_INCREMENT PRIMARY KEY /
	// SQLite INTEGER PRIMARY KEY AUTOINCREMENT）则不再追加独立 PRIMARY KEY 子句。
	create := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n%s\n)", table, strings.Join(cols, ",\n"))
	stmts := append([]string{create}, indexes...)
	return stmts
}

// dialectKind 返回方言种类字符串（用于列类型映射的类型 switch）。
// 不扩展 Dialect 接口，确保自定义方言无需实现迁移相关方法，保持向后兼容。
func dialectKind(d Dialect) string {
	switch d.(type) {
	case postgresDialect:
		return "postgres"
	case mysqlDialect:
		return "mysql"
	case sqliteDialect:
		return "sqlite"
	default:
		return "sqlite" // 未知方言按 SQLite 的宽松类型兜底，避免迁移直接失败
	}
}

// vectorOpsClass 把向量索引度量映射为 PG pgvector 的索引算子族（ops class）。
// hnsw / ivfflat 建索引时都必须指定算子族；缺省 cosine（最常用）。
func vectorOpsClass(metric string) string {
	switch metric {
	case "l2":
		return "vector_l2_ops"
	case "ip":
		return "vector_ip_ops"
	case "l1":
		return "vector_l1_ops"
	default:
		return "vector_cosine_ops"
	}
}

// columnSQL 根据字段 Go 类型与方言推导列定义（含主键/自增修饰，不含 UNIQUE）。
func columnSQL(f fieldInfo, d Dialect) string {
	kind := dialectKind(d)
	if f.vector {
		dim := f.vectorDim
		if dim == 0 {
			dim = 1536 // 未显式声明维度时的兜底；建议用 ,vector(N) 显式指定
		}
		switch kind {
		case "postgres":
			return fmt.Sprintf("vector(%d)", dim)
		case "mysql":
			return fmt.Sprintf("VECTOR(%d)", dim)
		default:
			return "TEXT" // SQLite 无原生向量类型，存文本；生产建议用 PG/MySQL
		}
	}
	if f.json {
		switch kind {
		case "postgres":
			return "JSONB"
		case "mysql":
			return "JSON"
		default:
			return "TEXT"
		}
	}
	// 主键 + 自增：以内联约束表达，避免重复 PRIMARY KEY
	if f.pk && f.autoInc {
		switch kind {
		case "postgres":
			if isInt64Type(f.typ) {
				return "BIGSERIAL PRIMARY KEY"
			}
			return "SERIAL PRIMARY KEY"
		case "mysql":
			return "BIGINT AUTO_INCREMENT PRIMARY KEY"
		default:
			return "INTEGER PRIMARY KEY AUTOINCREMENT"
		}
	}

	base := baseTypeSQL(f.typ, kind)
	if f.pk {
		return base + " PRIMARY KEY"
	}
	return base
}

// baseTypeSQL 把 Go 类型映射到方言无关的列类型（不含主键/自增修饰）。
func baseTypeSQL(t reflect.Type, kind string) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		if kind == "mysql" {
			return "TINYINT(1)"
		}
		return "BOOLEAN"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		if kind == "mysql" {
			return "INT"
		}
		return "INTEGER"
	case reflect.Int64, reflect.Uint64:
		if kind == "mysql" {
			return "BIGINT"
		}
		return "BIGINT"
	case reflect.Float32:
		if kind == "mysql" {
			return "FLOAT"
		}
		return "REAL"
	case reflect.Float64:
		if kind == "mysql" {
			return "DOUBLE"
		}
		return "DOUBLE PRECISION"
	case reflect.String:
		if kind == "mysql" {
			return "VARCHAR(255)"
		}
		return "TEXT"
	default:
		// time.Time / []byte / 未知类型：按惯例兜底
		switch {
		case isTimeType(t):
			if kind == "mysql" {
				return "DATETIME"
			}
			if kind == "sqlite" {
				return "DATETIME"
			}
			return "TIMESTAMP"
		case isBytesType(t):
			switch kind {
			case "postgres":
				return "BYTEA"
			case "mysql":
				return "BLOB"
			default:
				return "BLOB"
			}
		default:
			if kind == "mysql" {
				return "TEXT"
			}
			return "TEXT"
		}
	}
}

func isInt64Type(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Kind() == reflect.Int64 || t.Kind() == reflect.Uint64
}

func isBytesType(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8
}
