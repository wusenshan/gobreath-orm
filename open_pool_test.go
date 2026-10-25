package orm

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// 本文件固定「配置读写分离时主库被开了两个连接池」这一缺陷。
//
// 旧行为：Open 先按 cfg.DSN 开池 A 挂到 db.exec，newReadWriteRouter 又按同一份 DSN
// 开池 B 交给 router.primary。常规查询全部走 router，于是：
//   - 连接池参数只作用于池 A（几乎不参与查询），主库连接数却是 A+B 两份；
//   - db.SQL() 返回池 A：Stats() 看不到真实流量、运行期 SetMaxOpenConns 无效；
//   - db.SQL().Close() 关不掉 router 的主库与副本池。
//
// 这些都无法用「查询结果对不对」暴露（结果一直是对的），只能断言对象身份与池配置，
// 所以本文件的断言全部围绕 `db.SQL() == router.primary` 与 `Stats()`。

// poolCfg 生成一份启用读写分离、且 Primary 留空（走 cfg 默认）的配置：
// 这是最常见的写法，也正是此前开出两个同 DSN 池的路径。
func poolCfg(dsn string, replicas int) Config {
	cfg := Config{
		Driver:          "ormmock",
		DSN:             dsn,
		MaxOpenConns:    5,
		MaxIdleConns:    3,
		ConnMaxLifetime: 90 * time.Second,
		ConnMaxIdleTime: 45 * time.Second,
		ReadWrite:       &ReadWriteConfig{},
	}
	for i := 0; i < replicas; i++ {
		cfg.ReadWrite.Replicas = append(cfg.ReadWrite.Replicas,
			DataSource{Driver: "ormmock", DSN: dsn + "-replica"})
	}
	return cfg
}

// TestOpenReadWriteReusesPrimaryPool 同源主库只能有一个池：db.SQL() 与 router.primary
// 必须是同一个 *sql.DB 指针，且连接池参数要真正落到这个池上。
func TestOpenReadWriteReusesPrimaryPool(t *testing.T) {
	db, err := Open(poolCfg("pool-reuse", 1))
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer db.Close()

	if db.readWrite == nil {
		t.Fatal("配置了 ReadWrite 却没有路由器")
	}
	primary := db.SQL()
	if primary == nil {
		t.Fatal("db.SQL() 应返回主库池")
	}
	routerPrimary, ok := db.readWrite.primary.(*sql.DB)
	if !ok {
		t.Fatalf("router.primary 应是 *sql.DB，实际 %T", db.readWrite.primary)
	}
	if primary != routerPrimary {
		t.Fatal("db.SQL() 与 router.primary 必须是同一个池：否则连接池参数与 Stats() 都落在错的那一份上")
	}
	if got := primary.Stats().MaxOpenConnections; got != 5 {
		t.Fatalf("主库池 MaxOpenConnections 应为 5，实际 %d", got)
	}
	if got := primary.Stats().MaxIdleClosed; got != 0 {
		t.Fatalf("MaxIdleClosed 初始应为 0，实际 %d", got)
	}
}

// TestOpenReadWriteAppliesPoolSettingsToReplicas 连接池参数对副本同样生效。
// 旧实现只设主库，副本池退回 database/sql 默认（MaxOpenConns 无上限），
// 副本一多就会把连接数打爆，而配置里写的上限根本不生效。
func TestOpenReadWriteAppliesPoolSettingsToReplicas(t *testing.T) {
	db, err := Open(poolCfg("pool-replica", 2))
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer db.Close()

	if len(db.readWrite.replicas) != 2 {
		t.Fatalf("副本数量应为 2，实际 %d", len(db.readWrite.replicas))
	}
	for i, r := range db.readWrite.replicas {
		sqlDB, ok := r.(*sql.DB)
		if !ok {
			t.Fatalf("第 %d 个副本应是 *sql.DB，实际 %T", i, r)
		}
		if got := sqlDB.Stats().MaxOpenConnections; got != 5 {
			t.Fatalf("第 %d 个副本的 MaxOpenConnections 应为 5，实际 %d", i, got)
		}
		if sqlDB == db.SQL() {
			t.Fatalf("第 %d 个副本与主库池是同一个对象，副本没有独立开池", i)
		}
	}
}

// TestOpenDifferentPrimaryUsesRouterPool 主库指向另一个数据源时，db.exec 必须跟
// router.primary 一致 —— 否则 Transaction 会在另一个库上开事务，而普通查询走
// 路由，两者落在两个不同的数据库上。
func TestOpenDifferentPrimaryUsesRouterPool(t *testing.T) {
	db, err := Open(Config{
		Driver:       "ormmock",
		DSN:          "cfg-dsn-ignored",
		MaxOpenConns: 7,
		ReadWrite: &ReadWriteConfig{
			Primary:  DataSource{Driver: "ormmock", DSN: "primary-dsn"},
			Replicas: []DataSource{{Driver: "ormmock", DSN: "replica-dsn"}},
		},
	})
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer db.Close()

	if db.SQL() == nil || db.SQL() != db.readWrite.primary.(*sql.DB) {
		t.Fatal("主库不同源时 db.SQL() 仍必须等同 router.primary（事务与查询要落在同一个库）")
	}
	if got := db.SQL().Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("主库池 MaxOpenConnections 应为 7，实际 %d", got)
	}
}

// TestDBCloseClosesAllPools 关闭后主库与每个副本池都必须不可用。
// 旧实现只有 db.SQL().Close()，配了读写分离后副本池仍挂着连接。
func TestDBCloseClosesAllPools(t *testing.T) {
	db, err := Open(poolCfg("pool-close", 2))
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}

	pools := []*sql.DB{db.SQL()}
	for _, r := range db.readWrite.replicas {
		pools = append(pools, r.(*sql.DB))
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close 失败：%v", err)
	}
	for i, p := range pools {
		if err := p.Ping(); err == nil {
			t.Fatalf("第 %d 个池在 Close 之后仍可 Ping，说明没关掉", i)
		} else if !strings.Contains(err.Error(), "closed") {
			t.Fatalf("第 %d 个池的错误应表明已关闭，实际：%v", i, err)
		}
	}
	// *sql.DB.Close 幂等，二次调用不应报错。
	if err := db.Close(); err != nil {
		t.Fatalf("重复 Close 应安全，实际：%v", err)
	}
}

// TestOpenWithoutRouterStillAppliesPoolSettings 无路由时行为不变（防止重构误伤）。
func TestOpenWithoutRouterStillAppliesPoolSettings(t *testing.T) {
	db, err := Open(Config{Driver: "ormmock", DSN: "plain-dsn", MaxOpenConns: 4})
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer db.Close()

	if db.readWrite != nil {
		t.Fatal("未配置读写分离时不该建路由器")
	}
	if got := db.SQL().Stats().MaxOpenConnections; got != 4 {
		t.Fatalf("MaxOpenConnections 应为 4，实际 %d", got)
	}
}

// TestOpenReplicaOpenErrorFails 副本 DSN 打不开时 Open 必须失败（而不是带着
// 半个路由器返回）。这里用未注册的驱动名制造失败。
func TestOpenReplicaOpenErrorFails(t *testing.T) {
	_, err := Open(Config{
		Driver: "ormmock",
		DSN:    "pool-bad-replica",
		ReadWrite: &ReadWriteConfig{
			Replicas: []DataSource{{Driver: "no-such-driver-xyz", DSN: "x"}},
		},
	})
	if err == nil {
		t.Fatal("副本驱动不存在时 Open 必须报错")
	}
}
