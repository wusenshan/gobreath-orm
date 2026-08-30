// examples/vector-search/qwen/main.go
//
// 国内可用的向量模型接入：阿里云百炼（通义千问）text-embedding 系列。
//
// 为什么不用 OpenAI：国内网络访问 api.openai.com 不稳定，且需境外支付。
// 阿里千问 embedding 模型（兼容 OpenAI /v1/embeddings 接口）：
//   - text-embedding-v3：默认 1024 维（也支持 768/512/256/128/64）
//   - text-embedding-v4（Qwen3-Embedding）：2048/1536/1024(默认)/768/512/256/128/64
// 因此 gobreath-orm 的 embed() 与 OpenAI 版几乎一样，只改 base_url 和 model，
// 零额外依赖（只用 net/http + encoding/json）。
//
// 运行（需设置环境变量 DASHSCOPE_API_KEY）：
//   export DASHSCOPE_API_KEY=sk-xxxx
//   go run .
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// embed 调用阿里千问（百炼）OpenAI 兼容的 /v1/embeddings 接口，返回 []float32。
// model 例如 "text-embedding-v3" / "text-embedding-v4"。
func embed(text, model string) ([]float32, error) {
	// 百炼 OpenAI 兼容端点。华北2（北京）/ 新加坡 / 中国香港也有「业务空间专属域名」，
	// 性能与稳定性更好，详见阿里云文档把下面 baseURL 换成对应专属域名即可。
	const baseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

	body, _ := json.Marshal(map[string]any{
		"input": text,
		"model": model,
		// 可选：部分模型支持 dimensions 控制输出维度（Matryoshka 截断）。
		// 设了就要和建表 VECTOR(N)/vector(N) 的 N 一致，否则触发维度不匹配报错：
		// "dimensions": 1024,
	})
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("DASHSCOPE_API_KEY"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dashscope embeddings failed: %s %s", resp.Status, b)
	}

	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, io.EOF
	}
	v := out.Data[0].Embedding
	f := make([]float32, len(v))
	for i, x := range v {
		f[i] = float32(x)
	}
	return f, nil
}

func main() {
	apiKey := os.Getenv("DASHSCOPE_API_KEY")
	if apiKey == "" {
		fmt.Println("提示：设置 DASHSCOPE_API_KEY 后可真实调用；下面先打印维度要点。")
		fmt.Println("text-embedding-v3 默认输出 1024 维；text-embedding-v4 默认 1024 维。")
		fmt.Println("→ 建表时 vector(1024) / VECTOR(1024) 与之对齐；否则触发维度不匹配报错。")
		fmt.Println("→ 从 OpenAI(text-embedding-3-small=1536 维) 迁移过来时，务必把列维度改成 1024。")
		return
	}

	// 端到端接入：把文本 embedding 后写进 gobreath-orm 的 Article.Embedding，
	// 列定义为 vector(1024)/VECTOR(1024)，即可用 NearestBy 近邻检索（见 VECTOR.md 第 5 节）。
	vec, err := embed("Go 的并发模型与 goroutine 调度", "text-embedding-v3")
	if err != nil {
		fmt.Println("embed error:", err)
		return
	}
	first := vec
	if len(first) > 3 {
		first = first[:3]
	}
	fmt.Printf("embedding 维度 = %d（前 3 维: %v ...）\n", len(vec), first)
	fmt.Println("→ 写进 Article.Embedding，列定义为 vector(1024)/VECTOR(1024) 即可入库近邻检索。")
}
