package gen

import (
	"strings"
	"testing"
)

const pgDDL = `
CREATE TABLE user_info (
  id        serial PRIMARY KEY,
  name      varchar(64) NOT NULL,
  age       int,
  score     real,
  bio       text,
  active    bool,
  created   timestamptz,
  embedding vector(128)
);

CREATE TABLE "order_item" (
  "orderId"   bigserial PRIMARY KEY,
  "sku"       varchar(32) NOT NULL,
  "price"     numeric(10,2),
  "qty"       integer
);
`

const myDDL = `
CREATE TABLE product (
  id BIGINT NOT NULL AUTO_INCREMENT,
  title VARCHAR(255) NOT NULL,
  in_stock TINYINT(1) DEFAULT 1,
  price DOUBLE,
  created_at DATETIME,
  embedding VECTOR(768),
  PRIMARY KEY (id)
) ENGINE=InnoDB;
`

func TestDetectDialect(t *testing.T) {
	if got := DetectDialect(pgDDL); got != TypePostgres {
		t.Errorf("pgDDL 应识别为 Postgres，得到 %v", got)
	}
	if got := DetectDialect(myDDL); got != TypeMySQL {
		t.Errorf("myDDL 应识别为 MySQL，得到 %v", got)
	}
	if got := DetectDialect("CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT)"); got != TypeSQLite {
		t.Errorf("SQLite DDL 应识别为 SQLite，得到 %v", got)
	}
}

func TestParseDDL_Postgres(t *testing.T) {
	tables, err := ParseDDL(pgDDL, TypePostgres)
	if err != nil {
		t.Fatalf("ParseDDL: %v", err)
	}
	if len(tables) != 2 {
		t.Fatalf("期望 2 张表，得到 %d", len(tables))
	}
	users := tables[0]
	if users.Name != "user_info" || users.StructName != "UserInfo" {
		t.Errorf("表名/结构体名错误: %q / %q", users.Name, users.StructName)
	}
	want := map[string]Column{
		"Id":        {GoName: "Id", ColName: "id", GoType: "int", IsPK: true, IsAutoInc: true},
		"Name":      {GoName: "Name", ColName: "name", GoType: "string"},
		"Age":       {GoName: "Age", ColName: "age", GoType: "int"},
		"Score":     {GoName: "Score", ColName: "score", GoType: "float32"},
		"Bio":       {GoName: "Bio", ColName: "bio", GoType: "string"},
		"Active":    {GoName: "Active", ColName: "active", GoType: "bool"},
		"Created":   {GoName: "Created", ColName: "created", GoType: "time.Time"},
		"Embedding": {GoName: "Embedding", ColName: "embedding", GoType: "[]float32", IsVector: true, VectorDim: 128},
	}
	got := map[string]Column{}
	for _, c := range users.Columns {
		got[c.GoName] = c
	}
	for name, wc := range want {
		gc, ok := got[name]
		if !ok {
			t.Errorf("缺少字段 %s", name)
			continue
		}
		if gc != wc {
			t.Errorf("字段 %s 解析不符: 得到 %+v，期望 %+v", name, gc, wc)
		}
	}
	if !users.HasTime {
		t.Error("应检测到 time.Time 字段（import time）")
	}

	orders := tables[1]
	if orders.StructName != "OrderItem" {
		t.Errorf("带引号表名应转为 OrderItem，得到 %q", orders.StructName)
	}
	if len(orders.Columns) != 4 {
		t.Errorf("order_item 应有 4 列，得到 %d", len(orders.Columns))
	}
	// 引号列名 "orderId" -> OrderId
	if orders.Columns[0].GoName != "OrderId" || orders.Columns[0].ColName != "orderId" {
		t.Errorf("引号列名解析错误: %+v", orders.Columns[0])
	}
	// bigserial 应推断为 autoincrement（不能只匹配前缀 serial）
	if !orders.Columns[0].IsAutoInc {
		t.Errorf("bigserial 应推断为 autoincrement，得到 %+v", orders.Columns[0])
	}
}

func TestParseDDL_MySQL(t *testing.T) {
	tables, err := ParseDDL(myDDL, TypeMySQL)
	if err != nil {
		t.Fatalf("ParseDDL: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("期望 1 张表，得到 %d", len(tables))
	}
	p := tables[0]
	want := map[string]Column{
		"Id":        {GoName: "Id", ColName: "id", GoType: "int64", IsPK: true, IsAutoInc: true},
		"Title":     {GoName: "Title", ColName: "title", GoType: "string"},
		"InStock":   {GoName: "InStock", ColName: "in_stock", GoType: "int16"},
		"Price":     {GoName: "Price", ColName: "price", GoType: "float64"},
		"CreatedAt": {GoName: "CreatedAt", ColName: "created_at", GoType: "time.Time"},
		"Embedding": {GoName: "Embedding", ColName: "embedding", GoType: "[]float32", IsVector: true, VectorDim: 768},
	}
	got := map[string]Column{}
	for _, c := range p.Columns {
		got[c.GoName] = c
	}
	for name, wc := range want {
		gc, ok := got[name]
		if !ok {
			t.Errorf("缺少字段 %s", name)
			continue
		}
		if gc != wc {
			t.Errorf("字段 %s 不符: 得到 %+v，期望 %+v", name, gc, wc)
		}
	}
}

func TestFromDDL_OutputModes(t *testing.T) {
	cases := []struct {
		name string
		mode OutputMode
		want []string
	}{
		{"PerType", PerType, []string{"user_info.go", "user_info_cols.go"}},
		{"TwoFiles", TwoFiles, []string{"models.go", "columns.go"}},
		{"SingleFile", SingleFile, []string{"models_gen.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, err := FromDDL(pgDDL, Options{Package: "model", Dialect: TypeAuto, Mode: tc.mode})
			if err != nil {
				t.Fatalf("FromDDL: %v", err)
			}
			for _, w := range tc.want {
				if _, ok := files[w]; !ok {
					t.Errorf("缺少文件 %s，现有: %v", w, keys(files))
				}
			}
			// 生成内容应能通过 go/format 校验（FromDDL 内部已做），并含关键产物
			all := strings.Join(values(files), "\n")
			for _, needle := range []string{
				`db:"id,pk,autoincrement"`,
				`db:"embedding,vector(128)"`,
				`func (UserInfo) TableName() string { return "user_info" }`,
				`orm.ColOf[UserInfo]("Embedding")`,
			} {
				if !strings.Contains(all, needle) {
					t.Errorf("生成代码缺少 %q", needle)
				}
			}
		})
	}
}

func TestStructNames(t *testing.T) {
	src := `package m
type User struct { Name string }
type Order struct { Id int }
type notStruct int
`
	names, err := StructNames(src)
	if err != nil {
		t.Fatalf("StructNames: %v", err)
	}
	if len(names) != 2 || names[0] != "Order" || names[1] != "User" {
		t.Errorf("StructNames 结果错误: %v", names)
	}
}

func keys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func values(m map[string]string) []string {
	var vs []string
	for _, v := range m {
		vs = append(vs, v)
	}
	return vs
}
