package search

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/0xfederama/collections-app-backend/internal/items/core"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
)

// itemOf maps a search row to the item read model. The search and recent-items
// queries return the same columns as the items read, so the rows convert to one type.
func itemOf(r sqlc.SearchItemsRow) core.Item {
	it := core.Item{
		ID: r.ID, ListID: r.ListID, Title: r.Title, Type: r.Type,
		Price: priceOf(r.PriceMin, r.PriceMax, r.Currency), Website: r.WebsiteUrl,
		Metadata: json.RawMessage(r.Metadata), ContentVersion: int(r.ContentVersion), SortOrder: int(r.SortOrder),
		Version: int(r.Version), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	switch {
	case r.ImageAttachmentID.Valid:
		id := r.ImageAttachmentID.UUID
		img := &core.Image{
			Source: core.ImageSourceUpload, AttachmentID: id,
			URL: "/api/v1/attachments/" + id.String() + "/file", ThumbnailURL: "/api/v1/attachments/" + id.String() + "/thumbnail",
			OriginalFilename: r.ImageOriginalFilename,
		}
		if r.ImageWidth != nil {
			img.Width = int(*r.ImageWidth)
		}
		if r.ImageHeight != nil {
			img.Height = int(*r.ImageHeight)
		}
		it.Image = img
	case r.ImageUrl != nil:
		it.Image = &core.Image{Source: core.ImageSourceURL, URL: *r.ImageUrl}
	}
	return it
}

// priceOf is the price of a row; nil when both bounds are NULL.
func priceOf(minV, maxV pgtype.Numeric, currency *string) *core.Price {
	lo, hi := moneyOf(minV), moneyOf(maxV)
	if lo == nil && hi == nil {
		return nil
	}
	p := &core.Price{Min: lo, Max: hi}
	if currency != nil {
		p.Currency = *currency
	}
	return p
}

// moneyOf renders a stored NUMERIC as a two-decimal string ("1299.00"); nil for NULL.
// Integer arithmetic only, the scale is fixed at 2 (D-062).
func moneyOf(n pgtype.Numeric) *string {
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
	whole, frac := new(big.Int).QuoRem(new(big.Int).Abs(v), big.NewInt(100), new(big.Int))
	out := fmt.Sprintf("%s.%02d", whole, frac.Int64())
	if v.Sign() < 0 {
		out = "-" + out
	}
	return &out
}
