package dbcore

// Tests for the row estimate: the labels a pager shows, and the capability
// assertion an engine either implements or does not.

import (
	"context"
	"testing"
)

func TestEstimateLabel(t *testing.T) {
	cases := map[int64]string{
		-1:            "",
		0:             "~0",
		7:             "~7",
		999:           "~999",
		1000:          "~1K",
		1200:          "~1.2K",
		1500:          "~1.5K",
		999999:        "~1000K",
		1_000_000:     "~1M",
		1_234_567:     "~1.2M",
		12_345_678:    "~12.3M",
		1_000_000_000: "~1B",
		2_500_000_000: "~2.5B",
	}
	for rows, want := range cases {
		if got := EstimateLabel(rows); got != want {
			t.Errorf("EstimateLabel(%d) = %q, want %q", rows, got, want)
		}
	}
}

type estimatingDriver struct{ Driver }

func (estimatingDriver) EstimatedRows(context.Context, string, string) (int64, bool, bool, error) {
	return 42, true, true, nil
}

type plainDriver struct{ Driver }

func TestAsRowEstimator(t *testing.T) {
	if _, ok := AsRowEstimator(estimatingDriver{}); !ok {
		t.Fatal("a driver implementing RowEstimator is not detected")
	}
	if _, ok := AsRowEstimator(plainDriver{}); ok {
		t.Fatal("a driver without EstimatedRows is reported as an estimator")
	}
}
