package bench

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// batchTotal 每轮插入的固定行数 —— 只有总行数固定，不同分批大小的吞吐才可比。
//
// 取 500 而不是更大：分批场景里 chunk=1 的每轮迭代是 500 次数据库往返，
// 行数再大就会把 b.N 压到个位数，采样噪声直接盖过被测差异。
const batchTotal = 500

// batchChunkSizes 待评估的分批大小。
//
// 覆盖三个量级：逐行（1）、日常推荐区间（10~100）、单语句大块（250~500）。
// 更靠上（500+）不是不重要，而是受「单条 INSERT 的绑定变量数上限」约束：
// SQLite 32766、MySQL/PG 65535，本表 7 个可写列，理论上限约 4680 行（SQLite）——
// 那一段的实测数据见 integration/ 模块的批量上限测试。
var batchChunkSizes = []int{1, 10, 50, 100, 250, 500}

// BenchmarkBatchInsert 单一变量：分批大小。
//
// 这是横向基准里少有人测、但对使用方最有决策价值的一项：
// 分批太小 → 往返次数多；分批太大 → 撞绑定变量上限直接报错。
// 拐点位置决定了 WithChunkSize 该取什么默认值。
func BenchmarkBatchInsert(b *testing.B) {
	ctx := context.Background()
	payload := makeRows(batchTotal)

	for _, cs := range batchChunkSizes {
		chunk := cs

		b.Run(fmt.Sprintf("raw/chunk=%d", chunk), func(b *testing.B) {
			h := newRawHarness(b)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := rawInsertChunked(ctx, h.db, payload, chunk); err != nil {
					b.Fatalf("raw 分批插入失败：%v", err)
				}
			}
		})

		b.Run(fmt.Sprintf("gorm/chunk=%d", chunk), func(b *testing.B) {
			h := newGormHarness(b)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				users := toGormUsers(payload)
				if err := h.db.WithContext(ctx).CreateInBatches(users, chunk).Error; err != nil {
					b.Fatalf("gorm 分批插入失败：%v", err)
				}
			}
		})

		b.Run(fmt.Sprintf("gobreath/chunk=%d", chunk), func(b *testing.B) {
			h := newOrmHarness(b)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				users := toOrmUsers(payload)
				if err := ormInsertChunked(ctx, h.db, users, chunk); err != nil {
					b.Fatalf("gobreath 分批插入失败：%v", err)
				}
			}
		})
	}
}

// rawInsertChunked 手写 SQL 的分批插入。
// 与 ORM 层用同一个分批循环骨架，差异只在「构造语句」这一段。
//
// 语句用 strings.Builder 拼而不是 `+=`：后者对整批行数是 O(n²) 的字符串拷贝，
// 会把 raw 层的分配量抬到几十 MB，等于送给下界一个假的高水位。
func rawInsertChunked(ctx context.Context, db *sql.DB, rows []benchRow, chunk int) error {
	var sb strings.Builder
	for i := 0; i < len(rows); i += chunk {
		end := i + chunk
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[i:end]

		sb.Reset()
		sb.WriteString("INSERT INTO bench_users (name, age, city, email, score, created_at, updated_at, deleted_at) VALUES ")
		args := make([]any, 0, len(batch)*8)
		for j, r := range batch {
			if j > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("(?, ?, ?, ?, ?, ?, ?, NULL)")
			args = append(args, r.Name, r.Age, r.City, r.Email, r.Score, r.CreatedAt, r.UpdatedAt)
		}
		if _, err := db.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("chunk=%d 第 %d 批：%w", chunk, i/chunk, err)
		}
	}
	return nil
}

// ormInsertChunked 用 gobreath 的 BatchInsert 分批。
//
// 注意：框架目前的 BatchInsert **不做内部分批**，一次性把整个切片塞进一条 SQL。
// 所以这里显式切分 —— 这也正是「BatchInsert 该不该内置分批」这个待办
// 需要这批基准数据来做决定的原因。
func ormInsertChunked(ctx context.Context, db *orm.DB, users []User, chunk int) error {
	for i := 0; i < len(users); i += chunk {
		end := i + chunk
		if end > len(users) {
			end = len(users)
		}
		if err := orm.BatchInsert(ctx, db, users[i:end]); err != nil {
			return fmt.Errorf("chunk=%d 第 %d 批：%w", chunk, i/chunk, err)
		}
	}
	return nil
}

// toOrmUsers 把中立行转成 gobreath 实体（主键留零值，交给自增）。
func toOrmUsers(rows []benchRow) []User {
	out := make([]User, len(rows))
	for i, r := range rows {
		out[i] = User{
			Name: r.Name, Age: r.Age, City: r.City, Email: r.Email,
			Score: r.Score, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
	}
	return out
}

// toGormUsers 把中立行转成 GORM 实体。
func toGormUsers(rows []benchRow) []GormUser {
	out := make([]GormUser, len(rows))
	for i, r := range rows {
		out[i] = GormUser{
			Name: r.Name, Age: r.Age, City: r.City, Email: r.Email,
			Score: r.Score, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
	}
	return out
}
