// quickstart：30 秒跑通 gobreath-orm。
//
// 运行方式（纯 Go SQLite 驱动，无需安装任何数据库）：
//
//	cd examples/quickstart
//	go run .
package main

import (
	"context"
	"fmt"
	"os"

	orm "github.com/wusenshan/gobreath-orm"

	// 驱动由使用者自行导入，框架保持零依赖
	_ "modernc.org/sqlite"
)

// User 模型：列名从 db tag 推导，没写 tag 的字段按字段名 snake_case 推导
type User struct {
	Id   int64          `db:"id,pk,autoincrement"`
	Name string         // → name
	Age  int            // → age
	City string         // → city
	Meta map[string]any `db:"meta,json"` // JSON 列自动序列化/反序列化
}

// UserStat 非表结构体（DTO）：RawQuery 的结果直接落进来，
// 别名按 snake_case 命名即可命中字段，多余列自动忽略。
type UserStat struct {
	City  string `db:"city"`
	Count int64  `db:"cnt"`
}

func main() {
	ctx := context.Background()

	db, err := orm.Open(orm.Config{
		Driver: "sqlite",
		DSN:    "file:demo?mode=memory&cache=shared",
		// 内存库只有一条连接，顺便演示连接池参数：
		MaxOpenConns: 1,
		LogLevel:     orm.Info, // 打印执行的 SQL
	})
	if err != nil {
		fmt.Println("打开数据库失败:", err)
		os.Exit(1)
	}

	// 建表走 RawExec（DDL 也是原生 SQL 出口）
	if _, err := orm.RawExec(ctx, db, `
		CREATE TABLE users (
			id   INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			age  INTEGER NOT NULL,
			city TEXT NOT NULL,
			meta TEXT
		)`); err != nil {
		fmt.Println("建表失败:", err)
		os.Exit(1)
	}

	// 插入：自增主键自动回填，Meta 自动序列化为 JSON
	alice := &User{Name: "Alice", Age: 28, City: "Beijing", Meta: map[string]any{"vip": true}}
	if err := orm.Insert(ctx, db, alice); err != nil {
		fmt.Println("插入失败:", err)
		os.Exit(1)
	}
	fmt.Println("插入后主键回填: Id =", alice.Id)

	_ = orm.BatchInsert(ctx, db, []User{
		{Name: "Bob", Age: 17, City: "Shanghai"},
		{Name: "Carol", Age: 35, City: "Beijing"},
		{Name: "Dave", Age: 22, City: "Shenzhen"},
	})

	// 查询构造器：字段用闭包选择，调用点零字符串列名
	//   WHERE city = 'Beijing' AND age >= 18
	q := orm.NewQuery[User]().
		Eq(orm.Col[User](func(u *User) *string { return &u.City }), "Beijing").
		Ge(orm.Col[User](func(u *User) *int { return &u.Age }), 18)

	users, err := orm.SelectList(ctx, db, q)
	if err != nil {
		fmt.Println("查询失败:", err)
		os.Exit(1)
	}
	fmt.Println("\n北京成年用户:")
	for _, u := range users {
		fmt.Printf("  #%d %s age=%d meta=%v\n", u.Id, u.Name, u.Age, u.Meta)
	}

	// 原生 SQL + DTO：聚合报表这类复杂查询交给 RawQuery
	stats, err := orm.RawQuery[UserStat](ctx, db,
		`SELECT city, COUNT(*) AS cnt FROM users GROUP BY city ORDER BY cnt DESC`)
	if err != nil {
		fmt.Println("统计失败:", err)
		os.Exit(1)
	}
	fmt.Println("\n城市分布:")
	for _, s := range stats {
		fmt.Printf("  %s: %d 人\n", s.City, s.Count)
	}

	// 事务 + 更新 + 删除
	err = db.Transaction(ctx, func(tx *orm.DB) error {
		dave, err := orm.SelectOne(ctx, tx, orm.NewQuery[User]().
			Eq(orm.Col[User](func(u *User) *string { return &u.Name }), "Dave"))
		if err != nil {
			return err
		}
		dave.Age = 23
		return orm.UpdateById(ctx, tx, dave)
	})
	if err != nil {
		fmt.Println("事务失败:", err)
		os.Exit(1)
	}

	total, _ := orm.Count(ctx, db, orm.NewQuery[User]())
	fmt.Printf("\n事务更新完成，当前共 %d 个用户\n", total)
}
