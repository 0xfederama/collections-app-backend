package items

import (
	"fmt"
	"math/big"
	"regexp"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// Price is a price range in one currency. Min and Max are decimal strings with
// exactly two decimals ("1299.00", D-015); nil means that bound is unset.
type Price struct {
	Min, Max *string
	Currency string
}

var (
	moneyRe    = regexp.MustCompile(`^\d{1,10}\.\d{2}$`)
	currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
	hundred    = big.NewInt(100)
)

// money maps a "1299.00" string to NUMERIC(12,2) and back with integer
// arithmetic only: the digits are the big.Int, the scale is fixed at 2
// (Exp -2). No float is involved anywhere (D-062).

// moneyToNumeric parses a validated two-decimal string.
func moneyToNumeric(s string) pgtype.Numeric {
	digits := s[:len(s)-3] + s[len(s)-2:] // drop the dot
	v, _ := new(big.Int).SetString(digits, 10)
	return pgtype.Numeric{Int: v, Exp: -2, Valid: true}
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

func priceFromRow(minV, maxV pgtype.Numeric, currency *string) *Price {
	lo, hi := numericToMoney(minV), numericToMoney(maxV)
	if lo == nil && hi == nil {
		return nil
	}
	p := &Price{Min: lo, Max: hi}
	if currency != nil {
		p.Currency = *currency
	}
	return p
}

// validatePrice checks the D-015 rules and returns the price to store.
func validatePrice(p *Price) []apperr.FieldError {
	var errs []apperr.FieldError
	if p.Min == nil && p.Max == nil {
		errs = append(errs, apperr.FieldError{Field: "price", Message: "needs at least one of min and max"})
	}
	if p.Min != nil && !moneyRe.MatchString(*p.Min) {
		errs = append(errs, apperr.FieldError{Field: "price.min", Message: "must be a decimal with 2 decimals like 12.00"})
	}
	if p.Max != nil && !moneyRe.MatchString(*p.Max) {
		errs = append(errs, apperr.FieldError{Field: "price.max", Message: "must be a decimal with 2 decimals like 12.00"})
	}
	if !currencyRe.MatchString(p.Currency) {
		errs = append(errs, apperr.FieldError{Field: "price.currency", Message: "must be a 3-letter uppercase code like EUR"})
	}
	if len(errs) == 0 && p.Min != nil && p.Max != nil {
		if moneyToNumeric(*p.Min).Int.Cmp(moneyToNumeric(*p.Max).Int) > 0 {
			errs = append(errs, apperr.FieldError{Field: "price.max", Message: "must not be less than min"})
		}
	}
	return errs
}

// priceColumns converts a validated price (or nil) to the three columns.
func priceColumns(p *Price) (minV, maxV pgtype.Numeric, currency *string) {
	if p == nil {
		return pgtype.Numeric{}, pgtype.Numeric{}, nil
	}
	if p.Min != nil {
		minV = moneyToNumeric(*p.Min)
	}
	if p.Max != nil {
		maxV = moneyToNumeric(*p.Max)
	}
	c := p.Currency
	return minV, maxV, &c
}
