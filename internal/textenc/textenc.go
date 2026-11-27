// Package textenc normalizes source file bytes to UTF-8.
//
// Chinese Windows projects are frequently GBK/GB2312 even when the code itself
// is ASCII, so the tokenizer would otherwise turn comments and UI strings into
// replacement characters. UTF-8 files pass through unchanged; everything else
// is decoded as GB18030 (a superset of GBK/GB2312).
package textenc

import (
	"bytes"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Decode returns data as UTF-8. It strips a UTF-8 BOM and falls back to
// GB18030 when the input is not valid UTF-8.
func Decode(data []byte) string {
	data = bytes.TrimPrefix(data, utf8BOM)
	if utf8.Valid(data) {
		return string(data)
	}
	decoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), data)
	if err != nil {
		return string(data)
	}
	return string(decoded)
}
