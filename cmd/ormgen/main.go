package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wusenshan/gobreath-orm/gen"
)

func main() {
	var (
		types      = flag.String("type", "", "要生成列名集合的结构体类型，多个用逗号分隔，例如 User,Order")
		out        = flag.String("out", "", "输出文件名，默认 <第一个type>_cols.go")
		dir        = flag.String("dir", ".", "模型源码所在目录 / 生成文件输出目录")
		ddl        = flag.String("ddl", "", "DDL 文件路径（.sql）；指定后从建表语句生成模型+列闭包")
		pkg        = flag.String("pkg", "model", "生成代码的包名")
		mode       = flag.String("mode", "perType", "DDL 输出方式：perType / twoFiles / singleFile")
		tablePrefix = flag.String("table-prefix", "", "表前缀，仅作用于 TableName() 返回的物理表名")
		serve      = flag.Bool("serve", false, "启动本地 Web 生成器（ormgen serve）")
		addr       = flag.String("addr", ":8080", "serve 监听地址")
	)
	flag.Parse()

	if *serve {
		runServer(*addr)
		return
	}

	if *ddl != "" {
		runDDL(*ddl, *dir, *pkg, *mode, *tablePrefix)
		return
	}

	if *types == "" {
		fmt.Fprintln(os.Stderr, "用法: ormgen -type User[,Order] [-out user_cols.go] [-dir .]")
		fmt.Fprintln(os.Stderr, "     ormgen -ddl schema.sql [-pkg model] [-mode perType|twoFiles|singleFile] [-dir .]")
		os.Exit(1)
	}
	typeList := strings.Split(*types, ",")
	for i := range typeList {
		typeList[i] = strings.TrimSpace(typeList[i])
	}

	outFile := *out
	if outFile == "" {
		outFile = typeList[0] + "_cols.go"
	}

	if err := gen.Generate(*dir, typeList, outFile); err != nil {
		fmt.Fprintf(os.Stderr, "ormgen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("ormgen: 已生成 %s/%s\n", *dir, outFile)
}

func runDDL(path, dir, pkg, mode string, tablePrefix string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ormgen: 读取 DDL 文件: %v\n", err)
		os.Exit(1)
	}
	om := gen.PerType
	switch strings.ToLower(mode) {
	case "twofiles", "two":
		om = gen.TwoFiles
	case "singlefile", "single":
		om = gen.SingleFile
	}
	files, err := gen.FromDDL(string(data), gen.Options{
		Package:     pkg,
		Dialect:     gen.TypeAuto,
		Mode:        om,
		TablePrefix: tablePrefix,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ormgen: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "ormgen: 创建输出目录: %v\n", err)
		os.Exit(1)
	}
	for name, content := range files {
		outPath := filepath.Join(dir, name)
		if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "ormgen: 写入 %s: %v\n", outPath, err)
			os.Exit(1)
		}
		fmt.Printf("ormgen: 已生成 %s\n", outPath)
	}
}
