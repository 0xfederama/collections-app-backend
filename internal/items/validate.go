package items

import (
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// Field limits of DATA_MODEL §2 (the same rules are CHECK constraints).
const (
	maxTitleLen = 300
	maxURLLen   = 2048
)

var (
	moneyRe    = regexp.MustCompile(`^\d{1,10}\.\d{2}$`)
	currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
)

// CleanCreate checks the field rules of a new item (422, every field error at
// once) and returns the input with the title trimmed and the website normalized.
// The list and type rules come after it (PriceAllowed, ValidateMetadata).
func CleanCreate(in CreateInput) (CreateInput, error) {
	var errs []apperr.FieldError
	title, fe := cleanText("title", in.Title, 1, maxTitleLen)
	errs = appendIf(errs, fe)
	website, errs := optionalWebsite(errs, in.Website)
	if in.Price != nil {
		errs = append(errs, validatePrice(in.Price)...)
	}
	if err := validate(errs); err != nil {
		return CreateInput{}, err
	}
	out := in
	out.Title = title
	out.Website = website
	return out, nil
}

// CleanPatch checks the field rules of a patch (422) and returns it with the
// title trimmed and the website normalized. A nil price or website value is a
// clear and needs no check.
func CleanPatch(p Patch) (Patch, error) {
	var errs []apperr.FieldError
	if p.Title != nil {
		title, fe := cleanText("title", *p.Title, 1, maxTitleLen)
		p.Title = &title
		errs = appendIf(errs, fe)
	}
	if p.Website.Set && p.Website.Value != nil {
		site, fe := cleanWebsite(*p.Website.Value)
		p.Website.Value = &site
		errs = appendIf(errs, fe)
	}
	if p.Price.Set && p.Price.Value != nil {
		errs = append(errs, validatePrice(p.Price.Value)...)
	}
	if err := validate(errs); err != nil {
		return Patch{}, err
	}
	return p, nil
}

// ValidatePrice checks a price like CleanCreate does (422 on price.*). The lookup
// package writes a book's price in its own atomic create, so it needs the check alone.
func ValidatePrice(p *Price) error { return validate(validatePrice(p)) }

// validatePrice checks the D-015 rules plus the F-007 single price (amount).
func validatePrice(p *Price) []apperr.FieldError {
	var errs []apperr.FieldError
	if p.Amount != nil {
		if p.Min != nil || p.Max != nil {
			errs = append(errs, apperr.FieldError{Field: "price", Message: "amount cannot be combined with min or max"})
		} else if !moneyRe.MatchString(*p.Amount) {
			errs = append(errs, apperr.FieldError{Field: "price.amount", Message: "must be a decimal with 2 decimals like 12.00"})
		}
		if !currencyRe.MatchString(p.Currency) {
			errs = append(errs, apperr.FieldError{Field: "price.currency", Message: "must be a 3-letter uppercase code like EUR"})
		}
		return errs
	}
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
		if moneyCmp(*p.Min, *p.Max) > 0 {
			errs = append(errs, apperr.FieldError{Field: "price.max", Message: "must not be less than min"})
		}
	}
	return errs
}

// moneyCmp compares two validated two-decimal strings by value ("10.00" > "9.00").
func moneyCmp(a, b string) int {
	x, _ := new(big.Rat).SetString(a)
	y, _ := new(big.Rat).SetString(b)
	return x.Cmp(y)
}

func validate(errs []apperr.FieldError) error {
	if len(errs) == 0 {
		return nil
	}
	return apperr.Validation(errs...)
}

func appendIf(errs []apperr.FieldError, fe *apperr.FieldError) []apperr.FieldError {
	if fe != nil {
		return append(errs, *fe)
	}
	return errs
}

// cleanText trims s and checks its length in characters.
func cleanText(field, s string, minLen, maxLen int) (string, *apperr.FieldError) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n < minLen || n > maxLen {
		msg := fmt.Sprintf("must be at most %d characters", maxLen)
		if minLen > 0 {
			msg = fmt.Sprintf("must be %d to %d characters", minLen, maxLen)
		}
		return s, &apperr.FieldError{Field: field, Message: msg}
	}
	return s, nil
}

func optionalWebsite(errs []apperr.FieldError, in *string) (*string, []apperr.FieldError) {
	if in == nil {
		return nil, errs
	}
	s, fe := cleanWebsite(*in)
	return &s, appendIf(errs, fe)
}

// cleanWebsite accepts absolute http and https URLs only (`javascript:` and
// friends are rejected) of at most 2048 characters.
func cleanWebsite(s string) (string, *apperr.FieldError) {
	s = strings.TrimSpace(s)
	bad := func(msg string) (string, *apperr.FieldError) {
		return s, &apperr.FieldError{Field: "website", Message: msg}
	}
	if utf8.RuneCountInString(s) > maxURLLen {
		return bad(fmt.Sprintf("must be at most %d characters", maxURLLen))
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return bad("must be an http or https URL")
	}
	return s, nil
}
