// Package definitions manages workspace kind and field definitions.
package definitions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/google/uuid"
)

type DB interface {
	database.TxBeginner
	sqlc.DBTX
}
type Service struct {
	db          DB
	workspaceID uuid.UUID
}

func New(db DB, workspaceID uuid.UUID) *Service { return &Service{db: db, workspaceID: workspaceID} }

type Field struct {
	Key, Label, Type  string
	Position, Version int
	Locked            bool
}
type Kind struct {
	Key, Name           string
	AllowsPrice, Locked bool
	Version             int
	Fields              []Field
}
type CreateKind struct {
	Name        string
	AllowsPrice bool
}
type CreateField struct{ Label, Type string }

func (s *Service) List(ctx context.Context) ([]Kind, error) {
	q := sqlc.New(s.db)
	rows, err := q.ListKindDefinitions(ctx, s.workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Kind, 0, len(rows))
	for _, r := range rows {
		fs, err := q.ListKindFields(ctx, sqlc.ListKindFieldsParams{WorkspaceID: s.workspaceID, KindID: r.ID})
		if err != nil {
			return nil, err
		}
		k := kindRow(r)
		for _, f := range fs {
			k.Fields = append(k.Fields, fieldRow(f))
		}
		out = append(out, k)
	}
	return out, nil
}
func (s *Service) Create(ctx context.Context, in CreateKind) (Kind, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Kind{}, apperr.Validation(apperr.FieldError{Field: "name", Message: "is required"})
	}
	key := keyFor(name)
	var row sqlc.KindDefinition
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		var err error
		row, err = q.InsertKindDefinition(ctx, sqlc.InsertKindDefinitionParams{WorkspaceID: s.workspaceID, Key: key, Name: name, AllowsPrice: in.AllowsPrice, Now: time.Now().UTC()})
		return err
	})
	if err != nil {
		return Kind{}, err
	}
	return kindRow(row), nil
}
func (s *Service) Rename(ctx context.Context, key string, v int, name string) (Kind, error) {
	var out sqlc.KindDefinition
	return s.mutateKind(ctx, key, v, func(q *sqlc.Queries, cur sqlc.KindDefinition) error {
		if cur.IsLocked {
			return apperr.Validation(apperr.FieldError{Field: "kind", Message: "is locked"})
		}
		var e error
		out, e = q.UpdateKindDefinitionName(ctx, sqlc.UpdateKindDefinitionNameParams{ID: cur.ID, WorkspaceID: s.workspaceID, Name: strings.TrimSpace(name), Now: time.Now().UTC()})
		return e
	}, &out)
}
func (s *Service) mutateKind(ctx context.Context, key string, v int, fn func(*sqlc.Queries, sqlc.KindDefinition) error, out *sqlc.KindDefinition) (Kind, error) {
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		cur, e := q.GetKindDefinitionByKey(ctx, sqlc.GetKindDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: key})
		if e != nil {
			return apperr.NotFound("kind")
		}
		cur, e = q.LockKindDefinition(ctx, sqlc.LockKindDefinitionParams{WorkspaceID: s.workspaceID, ID: cur.ID})
		if e != nil {
			return e
		}
		if int(cur.Version) != v {
			return apperr.VersionConflict(int(cur.Version))
		}
		return fn(q, cur)
	})
	if err != nil {
		return Kind{}, err
	}
	return kindRow(*out), nil
}
func (s *Service) Delete(ctx context.Context, key string, v int) error {
	return database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		cur, e := q.GetKindDefinitionByKey(ctx, sqlc.GetKindDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: key})
		if e != nil {
			return apperr.NotFound("kind")
		}
		cur, e = q.LockKindDefinition(ctx, sqlc.LockKindDefinitionParams{WorkspaceID: s.workspaceID, ID: cur.ID})
		if e != nil {
			return e
		}
		if int(cur.Version) != v {
			return apperr.VersionConflict(int(cur.Version))
		}
		if cur.IsLocked {
			return apperr.Validation(apperr.FieldError{Field: "kind", Message: "is locked"})
		}
		lists, e := q.ListActiveListsByKind(ctx, sqlc.ListActiveListsByKindParams{WorkspaceID: s.workspaceID, Kind: key})
		if e != nil {
			return e
		}
		if len(lists) > 0 {
			return apperr.Validation(apperr.FieldError{Field: "kind", Message: fmt.Sprintf("is used by %d list(s), including %s", len(lists), lists[0].Name)})
		}
		return q.DeleteKindDefinition(ctx, sqlc.DeleteKindDefinitionParams{ID: cur.ID, WorkspaceID: s.workspaceID})
	})
}
func kindRow(r sqlc.KindDefinition) Kind {
	return Kind{Key: r.Key, Name: r.Name, AllowsPrice: r.AllowsPrice, Locked: r.IsLocked, Version: int(r.Version)}
}
func fieldRow(r sqlc.KindField) Field {
	return Field{Key: r.Key, Label: r.Label, Type: r.FieldType, Position: int(r.Position), Locked: r.IsLocked, Version: int(r.Version)}
}
func keyFor(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
