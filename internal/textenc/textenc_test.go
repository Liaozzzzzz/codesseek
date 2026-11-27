package textenc

import (
	"testing"
)

func TestDecodeKeepsUTF8(t *testing.T) {
	in := "// 用户登录失败处理\nfunction handleLoginError() {}\n"
	if got := Decode([]byte(in)); got != in {
		t.Fatalf("Decode changed UTF-8 input: %q", got)
	}
}

func TestDecodeKeepsASCII(t *testing.T) {
	in := "export function payOrder(amount) { return amount }\n"
	if got := Decode([]byte(in)); got != in {
		t.Fatalf("Decode changed ASCII input: %q", got)
	}
}

func TestDecodeGBK(t *testing.T) {
	// "用户登录失败处理" encoded as GBK.
	gbk := []byte{
		0xD3, 0xC3, 0xBB, 0xA7, 0xB5, 0xC7, 0xC2, 0xBC, 0xCA, 0xA7, 0xB0, 0xDC, 0xB4, 0xA6, 0xC0, 0xED,
	}
	want := "用户登录失败处理"
	if got := Decode(gbk); got != want {
		t.Fatalf("Decode(GBK) = %q, want %q", got, want)
	}
}

func TestDecodeStripsUTF8BOM(t *testing.T) {
	in := append([]byte{0xEF, 0xBB, 0xBF}, []byte("// 登录\n")...)
	if got := Decode(in); got != "// 登录\n" {
		t.Fatalf("Decode kept BOM: %q", got)
	}
}
