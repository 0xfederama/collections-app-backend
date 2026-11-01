package search

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Postgres implements Store with plain queries. It takes the workspace from the
// context (scope) and reads active rows only.
type Postgres struct{ q *sqlc.Queries }

var _ Store = (*Postgres)(nil)

// NewPostgres returns the search repository over db (a pool in production).
func NewPostgres(db sqlc.DBTX) *Postgres { return &Postgres{q: sqlc.New(db)} }

func (p *Postgres) SearchItems(ctx context.Context, like, q string, limit, offset int) ([]items.Item, error) {
	rows, err := p.q.SearchItems(ctx, sqlc.SearchItemsParams{
		WorkspaceID: scope.WorkspaceID(ctx), LikeQ: like, Q: q,
		PageLimit: int32(limit), PageOffset: int32(offset), //nolint:gosec // bounded by the service
	})
	if err != nil {
		return nil, fmt.Errorf("search items: %w", err)
	}
	out := make([]items.Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, itemOf(r))
	}
	return out, nil
}

func (p *Postgres) CountSearchItems(ctx context.Context, like string) (int, error) {
	total, err := p.q.CountSearchItems(ctx, sqlc.CountSearchItemsParams{WorkspaceID: scope.WorkspaceID(ctx), LikeQ: like})
	if err != nil {
		return 0, fmt.Errorf("count search items: %w", err)
	}
	return int(total), nil
}

func (p *Postgres) ListSearchFolders(ctx context.Context) ([]Folder, error) {
	rows, err := p.q.ListSearchFolders(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("search folders: %w", err)
	}
	out := make([]Folder, 0, len(rows))
	for _, f := range rows {
		out = append(out, Folder{ID: f.ID, Name: f.Name, ParentID: ptrUUID(f.ParentFolderID)})
	}
	return out, nil
}

func (p *Postgres) ListSearchLists(ctx context.Context) ([]List, error) {
	rows, err := p.q.ListSearchLists(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("search lists: %w", err)
	}
	out := make([]List, 0, len(rows))
	for _, l := range rows {
		var typeName *string
		if l.Type != nil {
			if def, ok := types.Lookup(*l.Type); ok {
				typeName = &def.Name
			}
		}
		out = append(out, List{
			ID: l.ID, Name: l.Name, FolderID: ptrUUID(l.FolderID), Icon: l.Icon, IconColor: l.IconColor, TypeName: typeName,
		})
	}
	return out, nil
}

func (p *Postgres) ListRecentItems(ctx context.Context, limit int) ([]items.Item, error) {
	rows, err := p.q.ListRecentItems(ctx, sqlc.ListRecentItemsParams{
		WorkspaceID: scope.WorkspaceID(ctx), PageLimit: int32(limit), //nolint:gosec // bounded by the service
	})
	if err != nil {
		return nil, fmt.Errorf("list recent items: %w", err)
	}
	out := make([]items.Item, 0, len(rows))
	for _, r := range rows {
		// The recent and search rows have the same columns, so they convert to one type.
		out = append(out, itemOf(sqlc.SearchItemsRow(r)))
	}
	return out, nil
}

// itemOf maps a search row to the item read model. The columns match the items
// read, so the mapping is the same as internal/items; search keeps its own copy
// so that items exports nothing for it.
func itemOf(r sqlc.SearchItemsRow) items.Item {
	it := items.Item{
		ID: r.ID, ListID: r.ListID, Title: r.Title, Type: r.Type,
		Price: priceOf(r.PriceMin, r.PriceMax, r.Currency), Website: r.WebsiteUrl,
		Metadata: json.RawMessage(r.Metadata), ContentVersion: int(r.ContentVersion), SortOrder: int(r.SortOrder),
		Version: int(r.Version), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	switch {
	case r.ImageAttachmentID.Valid:
		id := r.ImageAttachmentID.UUID
		img := &items.Image{
			Source: items.ImageSourceUpload, AttachmentID: id,
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
		it.Image = &items.Image{Source: items.ImageSourceURL, URL: *r.ImageUrl}
	}
	return it
}

// priceOf is the price of a row; nil when both bounds are NULL.
func priceOf(minV, maxV pgtype.Numeric, currency *string) *items.Price {
	lo, hi := moneyOf(minV), moneyOf(maxV)
	if lo == nil && hi == nil {
		return nil
	}
	p := &items.Price{Min: lo, Max: hi}
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

// ptrUUID converts a nullable uuid to a pointer (nil when NULL).
func ptrUUID(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	v := id.UUID
	return &v
}
