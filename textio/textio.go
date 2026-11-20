package textio

// Streaming decode + chunked CSV reading: the importer's memory stays bounded
// by one chunk instead of the whole file, and common non-UTF-8 encodings are
// handled at the byte level so rows never arrive mojibake.

import (
	"bytes"
	"io"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// StreamChunkSize is how many rows one streamed chunk carries. The client
// never holds more than one chunk plus its open transaction, and the first
// chunk doubles as the CREATE TABLE type-inference sample.
const StreamChunkSize = 1000

// sniffSize bounds the encoding probe. A prefix this large is decisive for
// UTF-8 vs GB18030 in practice and costs one read.
const sniffSize = 64 * 1024

// looksLikeUTF16NoBOM reports whether data looks like UTF-16 without a BOM.
// UTF-16 ASCII text has NUL at every other byte position: UTF-16LE puts the
// ASCII char in the low byte (NUL at odd indices), UTF-16BE puts it in the
// high byte (NUL at even indices). If more than 70% of the pairs have a NUL
// on one side, the file is likely UTF-16.
func looksLikeUTF16NoBOM(data []byte) (le bool, ok bool) {
	if len(data) < 8 {
		return false, false
	}
	evenNULs, oddNULs := 0, 0
	pairs := len(data) / 2
	for i := 0; i < len(data)-1; i += 2 {
		if data[i] == 0 {
			evenNULs++
		}
		if data[i+1] == 0 {
			oddNULs++
		}
	}
	if oddNULs > pairs*7/10 {
		return true, true // UTF-16LE: NULs at odd positions
	}
	if evenNULs > pairs*7/10 {
		return false, true // UTF-16BE: NULs at even positions
	}
	return false, false
}

// DecodingReader transparently decodes common non-UTF-8 import files: a BOM
// picks UTF-8 (stripped) or UTF-16; without a BOM the first sniffSize bytes
// are probed, and bytes that are not valid UTF-8 fall back to GB18030 (a
// superset of GBK and GB2312, the usual encoding of Chinese-language exports).
// A BOM-less UTF-16 file is detected by NUL density: UTF-16 ASCII text has NUL
// at every other byte position, which is a strong signal even without a BOM.
func DecodingReader(r io.Reader) io.Reader {
	head := make([]byte, 3)
	n, _ := io.ReadFull(r, head)
	head = head[:n]
	switch {
	case len(head) >= 3 && head[0] == 0xEF && head[1] == 0xBB && head[2] == 0xBF:
		return r // UTF-8 BOM: stripped, the rest is already UTF-8
	case len(head) >= 2 && head[0] == 0xFF && head[1] == 0xFE:
		// The probe read three bytes but the BOM is two: the third byte is
		// already data and must go back.
		return transform.NewReader(io.MultiReader(bytes.NewReader(head[2:]), r),
			unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder())
	case len(head) >= 2 && head[0] == 0xFE && head[1] == 0xFF:
		return transform.NewReader(io.MultiReader(bytes.NewReader(head[2:]), r),
			unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder())
	}

	sniff := make([]byte, sniffSize)
	m, _ := io.ReadFull(r, sniff)
	sniff = sniff[:m]
	combined := append(head, sniff...)

	// A UTF-16 file without a BOM passes utf8.Valid (NUL is valid UTF-8), so
	// it would be returned as-is with NUL-separated lines. Detect it by NUL
	// density: UTF-16 ASCII text has NUL at every other byte position.
	if le, ok := looksLikeUTF16NoBOM(combined); ok {
		decoder := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)
		if !le {
			decoder = unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)
		}
		return transform.NewReader(io.MultiReader(bytes.NewReader(combined), r),
			decoder.NewDecoder())
	}

	// A multi-byte rune split by the sniff boundary would fail utf8.Valid on a
	// genuine UTF-8 file; drop up to three trailing bytes before concluding.
	if !utf8.Valid(combined) {
		for k := 1; k <= 3 && k < len(combined); k++ {
			if utf8.Valid(combined[:len(combined)-k]) {
				combined = combined[:len(combined)-k]
				break
			}
		}
	}
	prefix := io.MultiReader(bytes.NewReader(combined), r)
	if utf8.Valid(combined) {
		return prefix
	}
	return transform.NewReader(prefix, simplifiedchinese.GB18030.NewDecoder())
}
