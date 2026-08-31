package orm

import (
	"context"
	"strings"
	"testing"
)

// ---- AutoMigrate 测试模型 ----

type MigrateUser struct {
	Id        int64   `db:"id,pk,autoincrement"`
	Name      string  `db:"name"`
	Email     string  `db:"email,unique"`
	Age       int     `db:"age,index"`
	Profile   string  `db:"profile,json"`
	DeletedAt int64   `db:"deleted_at"`
}

func (MigrateUser) TableName() string { return "users" }

type MigrateArticle struct {
	Id        int64     `db:"id,pk,autoincrement"`
	Title     string    `db:"title"`
	UserId    int64     `db:"user_id"`
	Embedding []float32 `db:"embedding,vector(1536)"`
}

func (MigrateArticle) TableName() string { return "articles" }

func TestMigrateStatementsPG(t *testing.T) {
	stmts := migrateStatements(getMeta[MigrateUser](), PG, "")
	if len(stmts) != 2 {
		t.Fatalf("PG 应生成 2 条 DDL（建表 + 二级索引），实际 %d: %v", len(stmts), stmts)
	}
	create := stmts[0]
	wantFrags := []string{
		`CREATE TABLE IF NOT EXISTS "users" (`,
		`"id" BIGSERIAL PRIMARY KEY`, // int64 自增主键
		`"name" TEXT`,
		`"email" TEXT UNIQUE`, // unique 约束
		`"age" INTEGER`,
		`"profile" JSONB`, // json 列
		`"deleted_at" BIGINT`,
	}
	for _, f := range wantFrags {
		if !strings.Contains(create, f) {
			t.Fatalf("PG 建表语句缺少 %q:\n%s", f, create)
		}
	}
	if !strings.Contains(stmts[1], `CREATE INDEX IF NOT EXISTS "idx_users_age" ON "users" ("age")`) {
		t.Fatalf("PG 应生成 age 二级索引，实际: %s", stmts[1])
	}
}

func TestMigrateStatementsMySQL(t *testing.T) {
	stmts := migrateStatements(getMeta[MigrateUser](), MySQL, "")
	create := stmts[0]
	wantFrags := []string{
		"CREATE TABLE IF NOT EXISTS `users` (",
		"`id` BIGINT AUTO_INCREMENT PRIMARY KEY",
		"`name` VARCHAR(255)",
		"`email` VARCHAR(255) UNIQUE",
		"`profile` JSON",
	}
	for _, f := range wantFrags {
		if !strings.Contains(create, f) {
			t.Fatalf("MySQL 建表语句缺少 %q:\n%s", f, create)
		}
	}
	if !strings.Contains(stmts[1], "CREATE INDEX IF NOT EXISTS `idx_users_age` ON `users` (`age`)") {
		t.Fatalf("MySQL 应生成 age 二级索引，实际: %s", stmts[1])
	}
}

func TestMigrateStatementsSQLite(t *testing.T) {
	stmts := migrateStatements(getMeta[MigrateUser](), SQLite, "")
	create := stmts[0]
	for _, f := range []string{
		`CREATE TABLE IF NOT EXISTS "users" (`,
		`"id" INTEGER PRIMARY KEY AUTOINCREMENT`,
		`"name" TEXT`,
		`"profile" TEXT`,
	} {
		if !strings.Contains(create, f) {
			t.Fatalf("SQLite 建表语句缺少 %q:\n%s", f, create)
		}
	}
}

func TestMigrateVectorColumn(t *testing.T) {
	stmts := migrateStatements(getMeta[MigrateArticle](), PG, "")
	if !strings.Contains(stmts[0], `"embedding" vector(1536)`) {
		t.Fatalf("PG 向量列应为 vector(1536)，实际:\n%s", stmts[0])
	}
	stmts = migrateStatements(getMeta[MigrateArticle](), MySQL, "")
	if !strings.Contains(stmts[0], "`embedding` VECTOR(1536)") {
		t.Fatalf("MySQL 向量列应为 VECTOR(1536)，实际:\n%s", stmts[0])
	}
	stmts = migrateStatements(getMeta[MigrateArticle](), SQLite, "")
	if !strings.Contains(stmts[0], `"embedding" TEXT`) {
		t.Fatalf("SQLite 向量列应退化为 TEXT，实际:\n%s", stmts[0])
	}
}

func TestMigratePrefix(t *testing.T) {
	// Article 未实现 TableName()，表名自动推导为 articles，前缀应生效
	stmts := migrateStatements(getMeta[Article](), PG, "t_")
	if !strings.Contains(stmts[0], `CREATE TABLE IF NOT EXISTS "t_articles"`) {
		t.Fatalf("带前缀应生成 t_articles，实际:\n%s", stmts[0])
	}
}

func TestMigrateExecutesViaMock(t *testing.T) {
	db := newMockDB(t)
	if err := db.AutoMigrate(context.Background(), &MigrateUser{}, &MigrateArticle{}); err != nil {
		t.Fatalf("AutoMigrate 执行失败: %v", err)
	}
	// 最后记录的语句应为建表语句（Mock 驱动只记录最后一条），证明 DDL 被依次执行
	if !strings.Contains(recQuery, "CREATE TABLE IF NOT EXISTS") {
		t.Fatalf("AutoMigrate 应执行建表语句，实际最后语句: %s", recQuery)
	}
}
