package textio

// 编码探测回归（§352）：GB18030（中文 Windows CSV 常见）/UTF-16 with BOM/
// UTF-16 无 BOM/UTF-8 BOM 都要解码成正确 UTF-8；否则列名带 BOM、中文乱码。

import (
	"io"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

func decodeAll(t *testing.T, raw []byte) string {
	t.Helper()
	got, err := io.ReadAll(DecodingReader(strings.NewReader(string(raw))))
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func TestDecodingReaderGB18030(t *testing.T) {
	utf8CSV := "name,city\n张三,北京\n"
	gb, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(utf8CSV))
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeAll(t, gb); got != utf8CSV {
		t.Fatalf("GB18030 decoded = %q, want %q", got, utf8CSV)
	}
}

func TestDecodingReaderUTF16BOM(t *testing.T) {
	utf8CSV := "id,label\n1,ok\n"
	utf16, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().Bytes([]byte(utf8CSV))
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeAll(t, utf16); got != utf8CSV {
		t.Fatalf("UTF-16 BOM decoded = %q", got)
	}
}

func TestDecodingReaderUTF16NoBOM(t *testing.T) {
	utf8CSV := "id,label\n1,ok\n"
	utf16, err := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder().Bytes([]byte(utf8CSV))
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeAll(t, utf16); got != utf8CSV {
		t.Fatalf("UTF-16 no-BOM decoded = %q", got)
	}
}

func TestDecodingReaderStripsUTF8BOM(t *testing.T) {
	bom := []byte{0xEF, 0xBB, 0xBF}
	raw := append(bom, []byte("name,id\n张三,1\n")...)
	got := decodeAll(t, raw)
	if strings.HasPrefix(got, "\ufeff") {
		t.Fatalf("UTF-8 BOM not stripped: %q", got)
	}
	if !strings.HasPrefix(got, "name,id") || !strings.Contains(got, "张三") {
		t.Fatalf("decoded = %q", got)
	}
}

func TestDecodingReaderPlainUTF8(t *testing.T) {
	raw := "name,city\n张三,北京\n"
	if got := decodeAll(t, []byte(raw)); got != raw {
		t.Fatalf("plain UTF-8 changed: %q", got)
	}
}
