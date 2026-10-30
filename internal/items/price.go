package items

import (
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
)

var hundred = big.NewInt(100)

// money maps a "1299.00" string to NUMERIC(12,2) and back with integer
// arithmetic only: the digits are the big.Int, the scale is fixed at 2
// (Exp -2). No float is involved anywhere (D-062).

// moneyToNumeric parses a validated two-decimal string.
func moneyToNumeric(s string) pgtype.Numeric {
	digits := s[:len(s)-3] + s[len(s)-2:] // drop the dot
	v, _ := new(big.Int).SetString(digits, 10)
	return pgtype.Numeric{Int: v, Exp: -2, Valid: true}
}

// PriceParts converts a price (or nil) to the three item columns. A single
// amount (Amount) is stored as Min = Max. Values use the D-062 mapping (moneyToNumeric).
// The lookup package writes a book's price in its own atomic create.
func PriceParts(p *Price) (minV, maxV pgtype.Numeric, currency *string) {
	if p == nil {
		return pgtype.Numeric{}, pgtype.Numeric{}, nil
	}
	lo, hi := p.Min, p.Max
	if p.Amount != nil {
		lo, hi = p.Amount, p.Amount
	}
	if lo != nil {
		minV = moneyToNumeric(*lo)
	}
	if hi != nil {
		maxV = moneyToNumeric(*hi)
	}
	c := p.Currency
	return minV, maxV, &c
}

// numericToMoney renders a stored NUMERIC as a two-decimal string; nil for NULL.
func numericToMoney(n pgtype.Numeric) *string {
	if !n.Valid || n.Int == nil {
		return nil
	}
	v := new(big.Int).Set(n.Int)
	switch {
	case n.Exp > -2:
		v.Mul(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n.Exp)+2), nil))
	case n.Exp < -2:
		v.Quo(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(-2-int64(n.Exp)), nil))
	}
	whole, frac := new(big.Int).QuoRem(new(big.Int).Abs(v), hundred, new(big.Int))
	out := fmt.Sprintf("%s.%02d", whole, frac.Int64())
	if v.Sign() < 0 {
		out = "-" + out
	}
	return &out
}
