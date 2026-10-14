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

func TestValidatePrice(t *testing.T) {
	s := func(v string) *string { return &v }
	tests := []struct {
		name  string
		price Price
		bad   []string
	}{
		{"ok min only", Price{Min: s("1.00"), Currency: "EUR"}, nil},
		{"ok equal", Price{Min: s("2.00"), Max: s("2.00"), Currency: "EUR"}, nil},
		{"min above max", Price{Min: s("2.01"), Max: s("2.00"), Currency: "EUR"}, []string{"price.max"}},
		{"numeric not lexical order", Price{Min: s("10.00"), Max: s("9.00"), Currency: "EUR"}, []string{"price.max"}},
		{"neither", Price{Currency: "EUR"}, []string{"price"}},
		{"bad currency", Price{Min: s("1.00"), Currency: "eur"}, []string{"price.currency"}},
		{"all bad", Price{Min: s("1"), Max: s("x"), Currency: ""}, []string{"price.min", "price.max", "price.currency"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validatePrice(&tt.price)
			if len(errs) != len(tt.bad) {
				t.Fatalf("errs = %v, want fields %v", errs, tt.bad)
			}
			for i, fe := range errs {
				if fe.Field != tt.bad[i] {
					t.Errorf("errs[%d].Field = %s, want %s", i, fe.Field, tt.bad[i])
				}
			}
		})
	}
}

func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`50%_\x`); got != `50\%\_\\x` {
		t.Fatalf("got %s", got)
	}
}

func TestCleanWebsite(t *testing.T) {
	for _, ok := range []string{"http://a.b", "https://a.b/c?d=e#f", " HTTPS://A.B "} {
		if _, fe := cleanWebsite(ok); fe != nil {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,x", "file:///etc/passwd", "ftp://a.b", "//a.b", "a.b", "https://", "https:///x", "", "http://a b.c"} {
		if _, fe := cleanWebsite(bad); fe == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
