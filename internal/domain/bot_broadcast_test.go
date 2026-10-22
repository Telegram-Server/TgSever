package domain

import (
	"errors"
	"math"
	"testing"
)

func TestStarsAmountExactNanostarArithmetic(t *testing.T) {
	amount := StarsAmount{}
	for range 10 {
		var err error
		amount, err = amount.Add(StarsAmount{Nanos: 100_000_000})
		if err != nil {
			t.Fatal(err)
		}
	}
	if amount != (StarsAmount{Amount: 1}) {
		t.Fatalf("ten tenths=%+v", amount)
	}
	remaining, err := amount.Sub(StarsAmount{Nanos: 1})
	if err != nil || remaining != (StarsAmount{Nanos: 999_999_999}) {
		t.Fatalf("borrow=%+v err=%v", remaining, err)
	}
	if _, err := (StarsAmount{Nanos: 1}).Sub(StarsAmount{Nanos: 2}); !errors.Is(err, ErrStarsInsufficient) {
		t.Fatalf("overdraft=%v", err)
	}
	if _, err := (StarsAmount{Amount: math.MaxInt64, Nanos: 999_999_999}).Add(StarsAmount{Nanos: 1}); !errors.Is(err, ErrStarsInvalidAmount) {
		t.Fatalf("overflow=%v", err)
	}
	if _, err := (StarsAmount{Nanos: -1}).Add(StarsAmount{}); !errors.Is(err, ErrStarsInvalidAmount) {
		t.Fatalf("invalid fractional amount=%v", err)
	}
}
