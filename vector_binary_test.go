package orm

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 本文件覆盖 MySQL 的 VECTOR **二进制读回**。
//
// 背景：MySQL 的 VECTOR 列底层是 BLOB，按小端序单精度浮点排布。也就是说
// `SELECT emb FROM t` 拿回的是裸字节而不是 "[..]" 文本 —— 只支持文本的读路径
// 会让向量列在 MySQL 上「写得进去、读不回来」。PG 走文本、MySQL 走二进制，
// 两条路都必须通，否则 VECTOR.md §7.3 那句「写入 / 读取向量 ✅」就是空头支票。
//
// 断言的字节样本取自 MySQL 官方文档原文，不是我构造的：
//
//	STRING_TO_VECTOR("[3.14,2024,18]")                    = 0xC3F548400000FD4400009041
//	STRING_TO_VECTOR('[2, 3, 5, 7]')                      = 0x00000040000040400000A0400000E040
//
// 逐字节核对（以 3.14 为例）：float32(3.14) = 0x4048F5C3 → 小端字节 C3 F5 48 40 ✔
func TestParseVectorBinaryFromDocSamples(t *testing.T) {
	cases := []struct {
		name string
		hexs string
		want []float32
	}{
		{"文档样例：3.14/2024/18", "C3F548400000FD4400009041", []float32{3.14, 2024, 18}},
		{"文档样例：2/3/5/7", "00000040000040400000A0400000E040", []float32{2, 3, 5, 7}},
		{"全零", "0000000000000000", []float32{0, 0}},
		{"负数", "000000C0", []float32{-2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := hex.DecodeString(c.hexs)
			if err != nil {
				t.Fatalf("测试样本不是合法十六进制：%v", err)
			}
			got, err := parseVectorBinary(raw, len(c.want))
			if err != nil {
				t.Fatalf("解码失败：%v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("解出 %d 维；期望 %d 维", len(got), len(c.want))
			}
			for i, w := range c.want {
				if float32(got[i]) != w {
					t.Errorf("第 %d 个分量 = %v；期望 %v", i, float32(got[i]), w)
				}
			}
		})
	}
}

// TestParseVectorBinaryErrors：长度不是 4 的倍数、或维度与声明不符时必须报错，
// 不能静默截断或补零（与 ,vector(N) 的既有口径一致）。
func TestParseVectorBinaryErrors(t *testing.T) {
	if _, err := parseVectorBinary([]byte{1, 2, 3}, 0); err == nil {
		t.Error("长度 3 不是 4 的倍数，应报错")
	} else if !strings.Contains(err.Error(), "4 的倍数") {
		t.Errorf("报错文案应点明 4 字节对齐，实际：%v", err)
	}

	raw, _ := hex.DecodeString("0000004000004040") // 2 个分量
	if _, err := parseVectorBinary(raw, 3); err == nil {
		t.Error("解出 2 维但字段声明 3 维，应报错")
	} else if !strings.Contains(err.Error(), "3 维") {
		t.Errorf("报错文案应点明声明维度，实际：%v", err)
	}

	// dim=0（db tag 未写 ,vector(N)）时不校验维度，能解多少算多少
	if got, err := parseVectorBinary(raw, 0); err != nil || len(got) != 2 {
		t.Errorf("dim=0 应跳过维度校验并解出 2 维，实际 %v / %v", got, err)
	}
}

// TestParseVectorPayloadDispatch：文本与二进制两种载荷都要走对分支。
func TestParseVectorPayloadDispatch(t *testing.T) {
	// ① PG 的文本形式
	if got, err := parseVectorPayload([]byte("[1,0,0]"), 3); err != nil {
		t.Errorf("文本载荷应走文本解析：%v", err)
	} else if len(got) != 3 || got[0] != 1 || got[1] != 0 {
		t.Errorf("文本载荷解析结果 = %v；期望 [1 0 0]", got)
	}

	// ② 前导空白也应识别为文本
	if _, err := parseVectorPayload([]byte("  [1,0,0]"), 3); err != nil {
		t.Errorf("带前导空白的文本载荷应被识别：%v", err)
	}

	// ③ MySQL 的二进制形式
	raw, _ := hex.DecodeString("C3F548400000FD4400009041")
	got, err := parseVectorPayload(raw, 3)
	if err != nil {
		t.Fatalf("二进制载荷应走二进制解析：%v", err)
	}
	if len(got) != 3 || float32(got[0]) != 3.14 || got[2] != 18 {
		t.Errorf("二进制载荷解析结果 = %v；期望 [3.14 2024 18]", got)
	}

	// ④ 空载荷 = 空切片，不是错误（SQL NULL 由上层 prepareTarget 处理）
	for _, b := range [][]byte{nil, {}} {
		if got, err := parseVectorPayload(b, 3); err != nil || got != nil {
			t.Errorf("空载荷应返回 nil / 无错，实际 %v / %v", got, err)
		}
	}

	// ⑤ 文本形式但内容坏掉 → 报错（而不是误当二进制去解）
	if _, err := parseVectorPayload([]byte("[1,abc]"), 0); err == nil {
		t.Error("文本载荷里的坏分量应报错")
	}
}

// TestVectorBinaryRoundTripThroughField：二进制载荷经 setVectorField 落到
// []float32 / []float64 / 定长数组三种字段上都要正确。
func TestVectorBinaryRoundTripThroughField(t *testing.T) {
	type binDoc struct {
		F32 []float32  `db:"f32,vector(3)"`
		F64 []float64  `db:"f64,vector(3)"`
		Arr [3]float32 `db:"arr,vector(3)"`
	}
	raw, _ := hex.DecodeString("C3F548400000FD4400009041") // 3.14 / 2024 / 18

	var d binDoc
	for name, fv := range map[string]reflect.Value{
		"F32": reflect.ValueOf(&d.F32).Elem(),
		"F64": reflect.ValueOf(&d.F64).Elem(),
		"Arr": reflect.ValueOf(&d.Arr).Elem(),
	} {
		if err := setVectorField(fv, raw, 3); err != nil {
			t.Fatalf("%s 字段写入失败：%v", name, err)
		}
	}
	if len(d.F32) != 3 || float32(d.F32[0]) != 3.14 || d.F32[1] != 2024 || d.F32[2] != 18 {
		t.Errorf("[]float32 字段 = %v；期望 [3.14 2024 18]", d.F32)
	}
	if len(d.F64) != 3 || d.F64[0] != float64(float32(3.14)) || d.F64[2] != 18 {
		t.Errorf("[]float64 字段 = %v；期望 [3.14 2024 18]", d.F64)
	}
	if float32(d.Arr[0]) != 3.14 || d.Arr[2] != 18 {
		t.Errorf("[3]float32 字段 = %v；期望 [3.14 2024 18]", d.Arr)
	}

	// 维度不符要报错，且不能改坏原值
	var bad binDoc
	if err := setVectorField(reflect.ValueOf(&bad.Arr).Elem(), raw, 4); err == nil {
		t.Error("声明 4 维但载荷是 3 维，应报错")
	}
}

// TestJSONVectorCoexist：同一结构体里 JSON 列与向量列并存时，各自走各自的赋值分支。
// 很容易被「向量读回」的改动误伤 —— setterFor 的分支顺序就在这两者之间。
func TestJSONVectorCoexist(t *testing.T) {
	type mixDoc struct {
		Meta map[string]any `db:"meta,json"`
		Emb  []float32      `db:"emb,vector(2)"`
	}
	m := &mixDoc{}
	// 走真实元数据 → setterFor 的分派，而不是手工指定赋值函数：
	// 要覆盖的正是 setterFor 里「向量分支排在 JSON 分支之前」这个顺序。
	set := map[string]scanSetter{}
	meta := getMeta[mixDoc]()
	for i := range meta.fields {
		f := &meta.fields[i]
		set[f.goName] = setterFor(f)
	}
	if err := set["Meta"](reflect.ValueOf(&m.Meta).Elem(), []byte(`{"a":1}`)); err != nil {
		t.Fatalf("JSON 列赋值失败：%v", err)
	}
	if err := set["Emb"](reflect.ValueOf(&m.Emb).Elem(), []byte{0, 0, 0, 64, 0, 0, 0, 192}); err != nil {
		t.Fatalf("向量列赋值失败：%v", err)
	}
	b, _ := json.Marshal(m.Meta)
	if string(b) != `{"a":1}` {
		t.Errorf("JSON 列 = %s；期望 {\"a\":1}", b)
	}
	if len(m.Emb) != 2 || m.Emb[0] != 2 || m.Emb[1] != -2 {
		t.Errorf("向量列 = %v；期望 [2 -2]", m.Emb)
	}
}
