package users

import "github.com/google/uuid"

// MeJSON is the wire shape of the caller's account (published as the schema "Me").
type MeJSON struct {
	ID        uuid.UUID `json:"id" format:"uuid"`
	Email     string    `json:"email" doc:"Lower-cased e-mail, the account key."`
	Name      *string   `json:"name" nullable:"true" doc:"Display name from the identity provider; null when unknown (show the e-mail)."`
	AvatarURL *string   `json:"avatar_url" nullable:"true" doc:"Absolute https URL of the avatar; null when unknown."`
	// DevIdentities lists the e-mails the dev switcher offers. Null unless the server runs with DEV_IDENTITY.
	DevIdentities []string `json:"dev_identities" nullable:"true" doc:"Dev/test only: the identities selectable with the X-Dev-User header or the dev_user cookie. Null in production."`
}

// MeOutput is the body of GET /me.
type MeOutput struct {
	Body MeJSON
}
