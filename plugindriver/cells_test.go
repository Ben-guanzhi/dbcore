package plugindriver

import (
	"testing"
	"time"

	"github.com/dbcore/dbcore"
)

// The tagged cell is the protocol's precision contract: JSON has one number
// type, so each kind must survive Encode→Decode unchanged — and an unknown
// kind must fail loudly rather than decode as NULL.
func TestValueCellRoundTrip(t *testing.T) {
	when := time.Date(2026, 10, 26, 8, 0, 0, 123456789, time.UTC)
	cases := []dbcore.Value{
		{Kind: dbcore.ValNull},
		{Kind: dbcore.ValInt, I: 9223372036854775807},
		{Kind: dbcore.ValFloat, F: 1.5},
		{Kind: dbcore.ValText, S: "héllo"},
		{Kind: dbcore.ValBlob, B: []byte{0, 1, 255}},
		{Kind: dbcore.ValBool, Bool: true},
		{Kind: dbcore.ValTime, T: when},
		// P3-2: the exact digits ride the text slot but keep their own kind.
		{Kind: dbcore.ValDecimal, S: "123456789012345678901234567890.123"},
	}
	for _, v := range cases {
		c := EncodeValue(v)
		got, err := DecodeValue(c)
		if err != nil {
			t.Fatalf("round trip of %+v: %v", v, err)
		}
		if got.Kind != v.Kind {
			t.Errorf("kind %v became %v", v.Kind, got.Kind)
		}
		if got.String() != v.String() {
			t.Errorf("text %q became %q", v.String(), got.String())
		}
	}
	if _, err := DecodeValue(CellPayload{Kind: "quantic"}); err == nil {
		t.Errorf("unknown kind must be an error, not a silent NULL")
	}
}
