// Package identity resolves who a request acts as (F-027): a verified Cloudflare Access token,
// else a dev identity, else the LAN owner fallback, else 401. It is the only place that knows
// about Access tokens and the headers Cloudflare adds.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
)

const (
	// refetchInterval is the least time between two fetches of the Access keys. go-oidc's
	// RemoteKeySet refetches on every bad signature, which a forged token could use to hammer
	// Cloudflare; this key set refetches at most once per interval, fetch failures included.
	refetchInterval = time.Minute
	fetchTimeout    = 5 * time.Second
	maxCertsBytes   = 1 << 20
)

// KeySet is an oidc.KeySet over the team's Access certs endpoint. Keys are cached; an unknown
// key id or a failed verification triggers at most one refetch per refetchInterval. A failed
// fetch keeps the old keys.
type KeySet struct {
	url    string
	client *http.Client
	clk    clock.Clock

	mu        sync.RWMutex
	keys      []jose.JSONWebKey
	lastFetch time.Time // last attempt, successful or not
	fetchMu   sync.Mutex
}

// NewKeySet returns the key set of the Access team (host without scheme). client may be nil.
func NewKeySet(teamDomain string, client *http.Client, clk clock.Clock) *KeySet {
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	if clk == nil {
		clk = clock.System{}
	}
	return &KeySet{url: "https://" + teamDomain + "/cdn-cgi/access/certs", client: client, clk: clk}
}

// VerifySignature implements oidc.KeySet. Algorithms, issuer, audience and expiry are checked
// by the oidc.IDTokenVerifier around it; only RS256 is parsed here as well.
func (k *KeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	jws, err := jose.ParseSignedCompact(jwt, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return nil, fmt.Errorf("parse jwt: %w", err)
	}
	if len(jws.Signatures) != 1 {
		return nil, errors.New("jwt must carry exactly one signature")
	}
	kid := jws.Signatures[0].Header.KeyID

	if payload, ok := k.verifyWith(jws, kid); ok {
		return payload, nil
	}
	// Unknown kid or bad signature: the keys may have rotated. Refetch, throttled.
	if !k.refetch(ctx) {
		return nil, errors.New("signature does not match a cached key")
	}
	if payload, ok := k.verifyWith(jws, kid); ok {
		return payload, nil
	}
	return nil, errors.New("signature does not match any key")
}

func (k *KeySet) verifyWith(jws *jose.JSONWebSignature, kid string) ([]byte, bool) {
	k.mu.RLock()
	keys := k.keys
	k.mu.RUnlock()
	for i := range keys {
		key := &keys[i]
		if (key.Use != "" && key.Use != "sig") || (kid != "" && key.KeyID != kid) {
			continue
		}
		if payload, err := jws.Verify(key); err == nil {
			return payload, true
		}
	}
	return nil, false
}

// refetch loads the keys unless one was attempted within refetchInterval. It reports whether it
// fetched new keys (a caller that waited on another fetch also gets true: the keys are fresh).
func (k *KeySet) refetch(ctx context.Context) bool {
	k.fetchMu.Lock()
	defer k.fetchMu.Unlock()
	k.mu.RLock()
	last := k.lastFetch
	k.mu.RUnlock()
	now := k.clk.Now()
	if !last.IsZero() && now.Sub(last) < refetchInterval {
		return false
	}
	keys, err := k.fetch(ctx)
	k.mu.Lock()
	k.lastFetch = now
	if err == nil {
		k.keys = keys
	}
	k.mu.Unlock()
	return err == nil
}

func (k *KeySet) fetch(ctx context.Context) ([]jose.JSONWebKey, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, nil)
	if err != nil {
		return nil, fmt.Errorf("build certs request: %w", err)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch certs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch certs: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCertsBytes))
	if err != nil {
		return nil, fmt.Errorf("read certs: %w", err)
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("decode certs: %w", err)
	}
	if len(set.Keys) == 0 {
		return nil, errors.New("certs endpoint returned no keys")
	}
	return set.Keys, nil
}
