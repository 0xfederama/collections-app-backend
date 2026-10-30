package items

import (
	"math/big"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestMoneyMapping(t *testing.T) {
	for _, s := range []string{"0.00", "0.01", "0.10", "12.30", "1299.00", "9999999999.99"} {
		n := moneyToNumeric(s)
		if !n.Valid || n.Exp != -2 {
			t.Fatalf("%s -> %+v", s, n)
		}
		if got := numericToMoney(n); got == nil || *got != s {
			t.Errorf("%s round-tripped to %v", s, got)
		}
	}
}

// pgx may hand back a NUMERIC with another exponent than the column's scale.
func TestNumericToMoneyNormalizesExponent(t *testing.T) {
	tests := []struct {
		name string
		n    pgtype.Numeric
		want string
	}{
		{"exp 0", pgtype.Numeric{Int: big.NewInt(1299), Exp: 0, Valid: true}, "1299.00"},
		{"exp positive", pgtype.Numeric{Int: big.NewInt(13), Exp: 2, Valid: true}, "1300.00"},
		{"exp -1", pgtype.Numeric{Int: big.NewInt(125), Exp: -1, Valid: true}, "12.50"},
		{"exp -3 exact", pgtype.Numeric{Int: big.NewInt(12500), Exp: -3, Valid: true}, "12.50"},
		{"zero", pgtype.Numeric{Int: big.NewInt(0), Exp: -2, Valid: true}, "0.00"},
		{"negative", pgtype.Numeric{Int: big.NewInt(-1250), Exp: -2, Valid: true}, "-12.50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := numericToMoney(tt.n); got == nil || *got != tt.want {
				t.Fatalf("got %v, want %s", got, tt.want)
			}
		})
	}
	if numericToMoney(pgtype.Numeric{}) != nil {
		t.Error("NULL must map to nil")
	}
}

func TestF007_AmountIsStoredAsMinEqualsMax(t *testing.T) {
	amount := "1200.00"
	p := Price{Amount: &amount, Currency: "EUR"}
	minV, maxV, cur := PriceParts(&p)
	for name, n := range map[string]pgtype.Numeric{"min": minV, "max": maxV} {
		if got := numericToMoney(n); got == nil || *got != amount {
			t.Errorf("%s = %v, want %s", name, got, amount)
		}
	}
	if cur == nil || *cur != "EUR" {
		t.Errorf("currency = %v", cur)
	}
	// What is read back never carries an amount: min and max are equal, and the same price compares equal.
	back := rowPrice(minV, maxV, cur)
	if back.Amount != nil || back.Min == nil || back.Max == nil || *back.Min != *back.Max {
		t.Fatalf("read back %+v", back)
	}
	if !samePrice(&p, back) {
		t.Error("an amount price must equal the same min == max price")
	}
	other := "1200.01"
	if samePrice(&p, &Price{Amount: &other, Currency: "EUR"}) || samePrice(&p, &Price{Amount: &amount, Currency: "USD"}) {
		t.Error("different amounts or currencies must differ")
	}
}
