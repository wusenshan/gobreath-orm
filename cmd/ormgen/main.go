package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/wusenshan/gobreath-orm/gen"
)

func main() {
	var (
		types = flag.String("type", "", "要生成列名集合的结构体类型，多个用逗号分隔，例如 User,Order")
		out   = flag.String("out", "", "输出文件名，默认 <第一个type>_cols.go")
		dir   = flag.String("dir", ".", "模型源码所在目录")
	)
	flag.Parse()

	if *types == "" {
		fmt.Fprintln(os.Stderr, "用法: ormgen -type User[,Order] [-out user_cols.go] [-dir .]")
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
