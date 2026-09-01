package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/wusenshan/gobreath-orm/gen"
)

//go:embed assets
var assetsFS embed.FS

type generateRequest struct {
	Source  string `json:"source"`
	Kind    string `json:"kind"` // "ddl" | "struct"
	Dialect string `json:"dialect"`
	Pkg     string `json:"pkg"`
	Mode    string `json:"mode"`
}

type generateResponse struct {
	Detected string            `json:"detected"`
	Files    map[string]string `json:"files"`
	Error    string            `json:"error,omitempty"`
}

type saveRequest struct {
	Dir         string            `json:"dir"`
	Files       map[string]string `json:"files"`
	SkipExisting bool             `json:"skipExisting"`
}

type saveResponse struct {
	Written []string `json:"written"`
	Skipped []string `json:"skipped"`
	Error   string   `json:"error,omitempty"`
}

func runServer(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data, err := assetsFS.ReadFile("assets/index.html")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(data)
	})
	mux.HandleFunc("/api/generate", generateHandler)
	mux.HandleFunc("/api/save", saveHandler)
	log.Printf("ormgen serve 已启动: http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func generateHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var req generateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, generateResponse{Error: err.Error()})
		return
	}
	resp := generateResponse{Files: map[string]string{}}
	switch req.Kind {
	case "ddl":
		dt := gen.TypeAuto
		switch strings.ToLower(req.Dialect) {
		case "postgres":
			dt = gen.TypePostgres
		case "mysql":
			dt = gen.TypeMySQL
		case "sqlite":
			dt = gen.TypeSQLite
		}
		if dt == gen.TypeAuto {
			resp.Detected = "DDL / " + dialectName(gen.DetectDialect(req.Source))
		} else {
			resp.Detected = "DDL / " + dialectName(dt)
		}
		om := gen.PerType
		switch strings.ToLower(req.Mode) {
		case "twofiles", "two":
			om = gen.TwoFiles
		case "singlefile", "single":
			om = gen.SingleFile
		}
		pkg := req.Pkg
		if pkg == "" {
			pkg = "model"
		}
		files, err := gen.FromDDL(req.Source, gen.Options{Package: pkg, Dialect: dt, Mode: om})
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Files = files
		}
	case "struct":
		names, err := gen.StructNames(req.Source)
		if err != nil {
			resp.Error = err.Error()
			writeJSON(w, resp)
			return
		}
		resp.Detected = fmt.Sprintf("Go struct / %d 个类型: %s", len(names), strings.Join(names, ", "))
		if len(names) == 0 {
			resp.Error = "未在输入中找到导出的结构体"
			writeJSON(w, resp)
			return
		}
		tmp, err := os.MkdirTemp("", "ormgen-*")
		if err != nil {
			resp.Error = err.Error()
			writeJSON(w, resp)
			return
		}
		defer os.RemoveAll(tmp)
		modelFile := filepath.Join(tmp, "model.go")
		if err := os.WriteFile(modelFile, []byte(req.Source), 0644); err != nil {
			resp.Error = err.Error()
			writeJSON(w, resp)
			return
		}
		outFile := "columns.go"
		if err := gen.Generate(tmp, names, outFile); err != nil {
			resp.Error = err.Error()
			writeJSON(w, resp)
			return
		}
		data, err := os.ReadFile(filepath.Join(tmp, outFile))
		if err != nil {
			resp.Error = err.Error()
			writeJSON(w, resp)
			return
		}
		resp.Files = map[string]string{outFile: string(data)}
	default:
		resp.Error = "kind 必须是 ddl 或 struct"
	}
	writeJSON(w, resp)
}

func saveHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var req saveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, saveResponse{Error: err.Error()})
		return
	}
	resp := saveResponse{}
	if err := os.MkdirAll(req.Dir, 0755); err != nil {
		resp.Error = err.Error()
		writeJSON(w, resp)
		return
	}
	for name, content := range req.Files {
		outPath := filepath.Join(req.Dir, name)
		if req.SkipExisting {
			if _, err := os.Stat(outPath); err == nil {
				resp.Skipped = append(resp.Skipped, name)
				continue
			}
		}
		if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
			resp.Error = err.Error()
			writeJSON(w, resp)
			return
		}
		resp.Written = append(resp.Written, name)
	}
	writeJSON(w, resp)
}

func dialectName(dt gen.DDLType) string {
	switch dt {
	case gen.TypePostgres:
		return "PostgreSQL"
	case gen.TypeMySQL:
		return "MySQL"
	case gen.TypeSQLite:
		return "SQLite"
	default:
		return "自动"
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}
