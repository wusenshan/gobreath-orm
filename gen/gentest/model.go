package gentest

//go:generate go run ../../cmd/ormgen -type User -out user_cols.go -dir .

type User struct {
	Id   int64  `db:"id,pk,autoincrement"`
	Name string
	Age  int
}
