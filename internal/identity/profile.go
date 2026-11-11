package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/users"
)

const (
	profileTimeout   = 3 * time.Second
	maxProfileBytes  = 1 << 20
	maxNameLen       = 200
	maxAvatarURLLen  = 2048
	identityEndpoint = "/cdn-cgi/access/get-identity"
)

// fetchProfile asks the team's get-identity endpoint who the token's owner is. The cookie
// carries the token; it is never logged. Name and picture are read from the top level or from
// oidc_fields, because the docs do not guarantee either.
func fetchProfile(ctx context.Context, client *http.Client, teamDomain, token string) (users.Profile, error) {
	ctx, cancel := context.WithTimeout(ctx, profileTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+teamDomain+identityEndpoint, nil)
	if err != nil {
		return users.Profile{}, fmt.Errorf("build identity request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: token})
	resp, err := client.Do(req)
	if err != nil {
		return users.Profile{}, fmt.Errorf("fetch identity: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return users.Profile{}, fmt.Errorf("fetch identity: status %d", resp.StatusCode)
	}
	var body struct {
		Name       string `json:"name"`
		Picture    string `json:"picture"`
		OIDCFields struct {
			Name    string `json:"name"`
			Picture string `json:"picture"`
		} `json:"oidc_fields"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxProfileBytes)).Decode(&body); err != nil {
		return users.Profile{}, fmt.Errorf("decode identity: %w", err)
	}
	var p users.Profile
	if name := strings.TrimSpace(firstNonEmpty(body.Name, body.OIDCFields.Name)); name != "" && len([]rune(name)) <= maxNameLen {
		p.Name = &name
	}
	if avatar := firstNonEmpty(body.Picture, body.OIDCFields.Picture); validAvatar(avatar) {
		p.AvatarURL = &avatar
	}
	return p, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// validAvatar keeps only an absolute https URL of at most 2048 characters.
func validAvatar(s string) bool {
	if s == "" || len(s) > maxAvatarURLLen {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
