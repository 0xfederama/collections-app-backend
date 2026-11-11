package identity_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/identity"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/users"
)

const (
	team = "team.cloudflareaccess.com"
	aud  = "aud-tag-1"
)

var testNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeUsers is an in-memory identity.Users.
type fakeUsers struct {
	mu      sync.Mutex
	byEmail map[string]users.User
	due     bool
	saved   []users.Profile
}

func newFakeUsers() *fakeUsers { return &fakeUsers{byEmail: map[string]users.User{}, due: true} }

func (f *fakeUsers) EnsureByEmail(_ context.Context, email string) (users.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	email = strings.ToLower(email)
	u, ok := f.byEmail[email]
	if !ok {
		u = users.User{ID: uuid.New(), Email: email}
		f.byEmail[email] = u
	}
	return u, nil
}

func (f *fakeUsers) ProfileDue(users.User) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.due }

func (f *fakeUsers) SaveProfile(_ context.Context, _ uuid.UUID, p users.Profile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, p)
	return nil
}

func (f *fakeUsers) emails() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for e := range f.byEmail {
		out = append(out, e)
	}
	return out
}

// access plays the team domain: the certs endpoint and get-identity.
type access struct {
	t         *testing.T
	key       *rsa.PrivateKey
	kid       string
	certHits  atomic.Int32
	certsDown atomic.Bool
	serveKeys atomic.Bool // false = the certs endpoint publishes no usable key
	identity  string      // get-identity body
	idStatus  int
	idCookie  atomic.Value
	srv       *httptest.Server
}

func newAccess(t *testing.T) *access {
	t.Helper()
	a := &access{t: t, key: genKey(t), kid: "kid-1", identity: `{}`, idStatus: 200}
	a.serveKeys.Store(true)
	a.srv = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.srv.Close)
	return a
}

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (a *access) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/cdn-cgi/access/certs":
		a.certHits.Add(1)
		if a.certsDown.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		keys := []jose.JSONWebKey{}
		if a.serveKeys.Load() {
			keys = append(keys, jose.JSONWebKey{Key: &a.key.PublicKey, KeyID: a.kid, Algorithm: "RS256", Use: "sig"})
		} else {
			keys = append(keys, jose.JSONWebKey{Key: &genKey(a.t).PublicKey, KeyID: "other", Algorithm: "RS256", Use: "sig"})
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: keys})
	case "/cdn-cgi/access/get-identity":
		if c, err := r.Cookie("CF_Authorization"); err == nil {
			a.idCookie.Store(c.Value)
		}
		w.WriteHeader(a.idStatus)
		_, _ = io.WriteString(w, a.identity)
	default:
		http.NotFound(w, r)
	}
}

// client sends every https://<team>/... request to the test server.
func (a *access) client() *http.Client {
	target, _ := url.Parse(a.srv.URL)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != team {
			a.t.Errorf("unexpected host %q", r.URL.Host)
		}
		r2 := r.Clone(r.Context())
		r2.URL.Scheme, r2.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r2)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type claims map[string]any

func (a *access) token(c claims) string { return signWith(a.t, a.key, a.kid, c) }

func signWith(t *testing.T, key *rsa.PrivateKey, kid string, c claims) string {
	t.Helper()
	opts := (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid)
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, opts)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(c)
	obj, err := s.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	out, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func good(email string) claims {
	return claims{
		"iss": "https://" + team, "aud": []string{aud}, "email": email,
		"iat": testNow.Unix(), "nbf": testNow.Add(-time.Minute).Unix(), "exp": testNow.Add(time.Hour).Unix(),
	}
}

type env struct {
	acc   *access
	users *fakeUsers
	clk   *fixedClock
	res   *identity.Resolver
	seen  []string // user ids per served request
	h     http.Handler
}

func newEnv(t *testing.T, mod func(*identity.Options)) *env {
	t.Helper()
	e := &env{acc: newAccess(t), users: newFakeUsers(), clk: &fixedClock{t: testNow}}
	o := identity.Options{
		TeamDomain: team, Audience: aud, Users: e.users, Clock: e.clk, HTTPClient: e.acc.client(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mod != nil {
		mod(&o)
	}
	e.res = identity.New(o)
	e.h = e.res.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := scope.UserID(r.Context())
		if !ok {
			t.Error("handler reached without a user")
		}
		e.seen = append(e.seen, id.String())
		w.WriteHeader(http.StatusNoContent)
	}))
	return e
}

func (e *env) do(path string, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func (e *env) jwt(t *testing.T, token string) map[string]string {
	t.Helper()
	return map[string]string{"Cf-Access-Jwt-Assertion": token}
}

func assertUnauthenticated(t *testing.T, rec *httptest.ResponseRecorder, token string) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var p struct {
		Code, Title, Detail string
		Status              int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != "unauthenticated" || p.Status != 401 || p.Detail != "Authentication is required." {
		t.Errorf("problem = %+v", p)
	}
	if token != "" && strings.Contains(rec.Body.String(), token) {
		t.Error("the 401 body echoes the token")
	}
}

func TestIdentity_ValidTokenCreatesLowerCasedUser(t *testing.T) {
	e := newEnv(t, nil)
	rec := e.do("/api/v1/me", e.jwt(t, e.acc.token(good("Alice@Example.TEST"))))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got := e.users.emails(); len(got) != 1 || got[0] != "alice@example.test" {
		t.Fatalf("users = %v", got)
	}
}

func TestIdentity_TokenRejected(t *testing.T) {
	other := genKey(t)
	e := newEnv(t, nil)
	mk := func(mod func(claims)) string {
		c := good("a@example.test")
		mod(c)
		return e.acc.token(c)
	}
	tests := []struct{ name, token string }{
		{"signed by an unknown key", signWith(t, other, "kid-1", good("a@example.test"))},
		{"signed by an unknown key and kid", signWith(t, other, "nope", good("a@example.test"))},
		{"wrong audience", mk(func(c claims) { c["aud"] = []string{"someone-else"} })},
		{"wrong issuer", mk(func(c claims) { c["iss"] = "https://evil.cloudflareaccess.com" })},
		{"expired", mk(func(c claims) { c["exp"] = testNow.Add(-time.Minute).Unix() })},
		{"not yet valid", mk(func(c claims) { c["nbf"] = testNow.Add(time.Hour).Unix() })},
		{"missing email", mk(func(c claims) { delete(c, "email") })},
		{"blank email", mk(func(c claims) { c["email"] = "  " })},
		{"garbage", "not-a-jwt"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.do("/api/v1/me", e.jwt(t, tt.token))
			assertUnauthenticated(t, rec, tt.token)
			if len(e.users.emails()) != 0 {
				t.Fatalf("a user was created: %v", e.users.emails())
			}
		})
	}
}

func TestIdentity_BadTokenNeverFallsThrough(t *testing.T) {
	e := newEnv(t, func(o *identity.Options) {
		o.DevIdentities = []string{"owner@dev.test"}
		o.LANOwnerEmail = "owner@example.test"
	})
	assertUnauthenticated(t, e.do("/api/v1/me", e.jwt(t, "garbage")), "garbage")
	if len(e.seen) != 0 {
		t.Fatal("handler reached")
	}
}

func TestIdentity_TokenWithoutAccessConfigIs401(t *testing.T) {
	e := newEnv(t, func(o *identity.Options) {
		o.TeamDomain, o.Audience = "", ""
		o.DevIdentities = []string{"owner@dev.test"}
	})
	assertUnauthenticated(t, e.do("/api/v1/me", e.jwt(t, "anything")), "anything")
}

func TestIdentity_ForgedPlainEmailHeaderIsIgnored(t *testing.T) {
	forged := map[string]string{"Cf-Access-Authenticated-User-Email": "owner@example.test"}
	// Nothing configured but Access: the plain header opens nothing.
	e := newEnv(t, nil)
	assertUnauthenticated(t, e.do("/api/v1/me", forged), "")
	// With a dev identity the first dev user answers, never the forged one.
	d := newEnv(t, func(o *identity.Options) { o.DevIdentities = []string{"owner@dev.test", "alice@dev.test"} })
	if rec := d.do("/api/v1/me", forged); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := d.users.emails(); len(got) != 1 || got[0] != "owner@dev.test" {
		t.Fatalf("users = %v", got)
	}
	// Even next to a bad token.
	h := map[string]string{"Cf-Access-Authenticated-User-Email": "owner@example.test", "Cf-Access-Jwt-Assertion": "bad"}
	assertUnauthenticated(t, e.do("/api/v1/me", h), "bad")
}

func TestIdentity_DevIdentity(t *testing.T) {
	devs := []string{"owner@dev.test", "alice@dev.test", "bob@dev.test"}
	tests := []struct {
		name    string
		hdr     map[string]string
		cookies []*http.Cookie
		want    string // "" = 401
	}{
		{"absent is the first identity", nil, nil, "owner@dev.test"},
		{"header", map[string]string{"X-Dev-User": "alice@dev.test"}, nil, "alice@dev.test"},
		{"cookie", nil, []*http.Cookie{{Name: "dev_user", Value: "bob@dev.test"}}, "bob@dev.test"},
		{"cookie url encoded", nil, []*http.Cookie{{Name: "dev_user", Value: "bob%40dev.test"}}, "bob@dev.test"},
		{"header wins over cookie", map[string]string{"X-Dev-User": "alice@dev.test"}, []*http.Cookie{{Name: "dev_user", Value: "bob@dev.test"}}, "alice@dev.test"},
		{"unknown header email", map[string]string{"X-Dev-User": "mallory@example.test"}, nil, ""},
		{"unknown cookie email", nil, []*http.Cookie{{Name: "dev_user", Value: "mallory@example.test"}}, ""},
		{"empty header", map[string]string{"X-Dev-User": ""}, nil, ""},
		{"unknown header beats a good cookie", map[string]string{"X-Dev-User": "mallory@example.test"}, []*http.Cookie{{Name: "dev_user", Value: "bob@dev.test"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, func(o *identity.Options) { o.DevIdentities = devs })
			rec := e.do("/api/v1/me", tt.hdr, tt.cookies...)
			if tt.want == "" {
				assertUnauthenticated(t, rec, "")
				return
			}
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := e.users.emails(); len(got) != 1 || got[0] != tt.want {
				t.Fatalf("users = %v, want [%s]", got, tt.want)
			}
		})
	}
}

func TestIdentity_DevHeaderIgnoredWhenDevIdentityOff(t *testing.T) {
	e := newEnv(t, nil)
	assertUnauthenticated(t, e.do("/api/v1/me", map[string]string{"X-Dev-User": "alice@dev.test"}), "")
}

func TestIdentity_LANOwnerFallback(t *testing.T) {
	on := func(o *identity.Options) { o.LANOwnerEmail = "owner@example.test" }
	tests := []struct {
		name   string
		mod    func(*identity.Options)
		hdr    map[string]string
		wantOK bool
	}{
		{"on, plain LAN request", on, nil, true},
		{"on, Cf-Ray present", on, map[string]string{"Cf-Ray": "abc-MXP"}, false},
		{"on, Cf-Connecting-Ip present", on, map[string]string{"Cf-Connecting-Ip": "1.2.3.4"}, false},
		{"on, empty Cf-Ray still counts", on, map[string]string{"Cf-Ray": ""}, false},
		{"off", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, tt.mod)
			rec := e.do("/api/v1/me", tt.hdr)
			if !tt.wantOK {
				assertUnauthenticated(t, rec, "")
				return
			}
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := e.users.emails(); len(got) != 1 || got[0] != "owner@example.test" {
				t.Fatalf("users = %v", got)
			}
		})
	}
}

func TestIdentity_DevBeatsLANFallback(t *testing.T) {
	e := newEnv(t, func(o *identity.Options) {
		o.DevIdentities = []string{"owner@dev.test"}
		o.LANOwnerEmail = "owner@example.test"
	})
	e.do("/api/v1/me", nil)
	if got := e.users.emails(); len(got) != 1 || got[0] != "owner@dev.test" {
		t.Fatalf("users = %v", got)
	}
}

func TestIdentity_OnlyAPIPathsAreChecked(t *testing.T) {
	e := newEnv(t, nil)
	for _, p := range []string{"/healthz", "/readyz", "/admin/logs", "/admin/logs/view", "/api/v1x/me", "/"} {
		if rec := httptest.NewRecorder(); true {
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, p, nil)
			passed := false
			e.res.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed = true })).ServeHTTP(rec, r)
			if !passed || rec.Code != http.StatusOK {
				t.Errorf("%s: passed=%v code=%d, want the request to reach the handler untouched", p, passed, rec.Code)
			}
		}
	}
	assertUnauthenticated(t, e.do("/api/v1/folders", nil), "")
	assertUnauthenticated(t, e.do("/api/v1/does-not-exist", nil), "")
}

func TestIdentity_KeysAreThrottledAndRotationWorks(t *testing.T) {
	e := newEnv(t, nil)
	good1 := e.acc.token(good("a@example.test"))
	if rec := e.do("/api/v1/me", e.jwt(t, good1)); rec.Code != http.StatusNoContent {
		t.Fatalf("first: %d", rec.Code)
	}
	if n := e.acc.certHits.Load(); n != 1 {
		t.Fatalf("cert fetches after first request = %d, want 1", n)
	}
	// Cached keys: no refetch.
	e.do("/api/v1/me", e.jwt(t, good1))
	if n := e.acc.certHits.Load(); n != 1 {
		t.Fatalf("cert fetches with a cached key = %d, want 1", n)
	}
	// A flood of forged tokens triggers no fetch inside the interval.
	forger := genKey(t)
	for range 20 {
		e.do("/api/v1/me", e.jwt(t, signWith(t, forger, "kid-1", good("a@example.test"))))
		e.do("/api/v1/me", e.jwt(t, signWith(t, forger, "kid-"+uuid.NewString(), good("a@example.test"))))
	}
	if n := e.acc.certHits.Load(); n != 1 {
		t.Fatalf("forged tokens caused %d fetches, want 1 in total", n)
	}
	// After the interval, one forged token costs one fetch, and a rotated key is picked up.
	e.clk.Advance(61 * time.Second)
	e.acc.key, e.acc.kid = genKey(t), "kid-2"
	e.do("/api/v1/me", e.jwt(t, signWith(t, forger, "kid-9", good("a@example.test"))))
	e.do("/api/v1/me", e.jwt(t, signWith(t, forger, "kid-9", good("a@example.test"))))
	if n := e.acc.certHits.Load(); n != 2 {
		t.Fatalf("fetches after the interval = %d, want 2", n)
	}
	if rec := e.do("/api/v1/me", e.jwt(t, e.acc.token(good("a@example.test")))); rec.Code != http.StatusNoContent {
		t.Fatalf("rotated key rejected: %d", rec.Code)
	}
}

func TestIdentity_RotationPicksUpNewKeyAtOnce(t *testing.T) {
	e := newEnv(t, nil)
	e.do("/api/v1/me", e.jwt(t, e.acc.token(good("a@example.test")))) // loads kid-1
	e.clk.Advance(2 * time.Minute)
	e.acc.key, e.acc.kid = genKey(t), "kid-2"
	if rec := e.do("/api/v1/me", e.jwt(t, e.acc.token(good("a@example.test")))); rec.Code != http.StatusNoContent {
		t.Fatalf("token of a rotated key rejected: %d", rec.Code)
	}
}

func TestIdentity_KeepsOldKeysWhenFetchFails(t *testing.T) {
	e := newEnv(t, nil)
	tok := e.acc.token(good("a@example.test"))
	e.do("/api/v1/me", e.jwt(t, tok))
	e.clk.Advance(2 * time.Minute)
	e.acc.certsDown.Store(true)
	// A forged token forces a failing refetch; the cached key still verifies the real one.
	assertUnauthenticated(t, e.do("/api/v1/me", e.jwt(t, signWith(t, genKey(t), "x", good("a@example.test")))), "")
	if rec := e.do("/api/v1/me", e.jwt(t, tok)); rec.Code != http.StatusNoContent {
		t.Fatalf("cached key lost after a failed fetch: %d", rec.Code)
	}
}

func TestIdentity_CertsUnavailableAtStartIs401(t *testing.T) {
	e := newEnv(t, nil)
	e.acc.certsDown.Store(true)
	assertUnauthenticated(t, e.do("/api/v1/me", e.jwt(t, e.acc.token(good("a@example.test")))), "")
}

func TestIdentity_ProfileFetchedInBackground(t *testing.T) {
	e := newEnv(t, nil)
	e.acc.identity = `{"name":"Alice A","picture":"https://img.example.test/a.png"}`
	tok := e.acc.token(good("a@example.test"))
	if rec := e.do("/api/v1/me", e.jwt(t, tok)); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	e.res.Wait()
	if len(e.users.saved) != 1 || e.users.saved[0].Name == nil || *e.users.saved[0].Name != "Alice A" ||
		e.users.saved[0].AvatarURL == nil || *e.users.saved[0].AvatarURL != "https://img.example.test/a.png" {
		t.Fatalf("saved = %+v", e.users.saved)
	}
	if got, _ := e.acc.idCookie.Load().(string); got != tok {
		t.Error("get-identity was not called with the CF_Authorization cookie")
	}
}

func TestIdentity_ProfileOnlyWhenDue(t *testing.T) {
	e := newEnv(t, nil)
	e.users.due = false
	e.do("/api/v1/me", e.jwt(t, e.acc.token(good("a@example.test"))))
	e.res.Wait()
	if len(e.users.saved) != 0 {
		t.Fatal("profile fetched although not due")
	}
	// Dev and LAN identities have no token, so nothing is fetched.
	d := newEnv(t, func(o *identity.Options) { o.DevIdentities = []string{"owner@dev.test"} })
	d.do("/api/v1/me", nil)
	d.res.Wait()
	if len(d.users.saved) != 0 {
		t.Fatal("profile fetched for a dev identity")
	}
}

func TestIdentity_ProfileFailureNeverFailsTheRequest(t *testing.T) {
	for name, set := range map[string]func(*access){
		"status 500":    func(a *access) { a.idStatus = 500 },
		"bad json":      func(a *access) { a.identity = `{` },
		"unauthorized":  func(a *access) { a.idStatus = 401 },
		"empty profile": func(a *access) { a.identity = `{}` },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, nil)
			set(e.acc)
			if rec := e.do("/api/v1/me", e.jwt(t, e.acc.token(good("a@example.test")))); rec.Code != http.StatusNoContent {
				t.Fatalf("status %d", rec.Code)
			}
			e.res.Wait()
			if name != "empty profile" && len(e.users.saved) != 0 {
				t.Fatalf("saved %+v after a failed fetch", e.users.saved)
			}
		})
	}
}

func TestIdentity_ProfileFieldsAndAvatarRules(t *testing.T) {
	long := "https://img.example.test/" + strings.Repeat("a", 2100)
	tests := []struct {
		name, body string
		wantName   string
		wantAvatar string
	}{
		{"top level", `{"name":"N","picture":"https://x.test/p.png"}`, "N", "https://x.test/p.png"},
		{"oidc_fields", `{"oidc_fields":{"name":"O","picture":"https://x.test/o.png"}}`, "O", "https://x.test/o.png"},
		{"top level wins", `{"name":"T","oidc_fields":{"name":"O"}}`, "T", ""},
		{"http avatar dropped", `{"name":"N","picture":"http://x.test/p.png"}`, "N", ""},
		{"relative avatar dropped", `{"name":"N","picture":"/p.png"}`, "N", ""},
		{"javascript avatar dropped", `{"name":"N","picture":"javascript:alert(1)"}`, "N", ""},
		{"too long avatar dropped", `{"name":"N","picture":"` + long + `"}`, "N", ""},
		{"avatar with userinfo dropped", `{"picture":"https://u:p@x.test/p.png"}`, "", ""},
		{"nothing", `{"email":"a@example.test"}`, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, nil)
			e.acc.identity = tt.body
			e.do("/api/v1/me", e.jwt(t, e.acc.token(good("a@example.test"))))
			e.res.Wait()
			if len(e.users.saved) != 1 {
				t.Fatalf("saved = %+v", e.users.saved)
			}
			p := e.users.saved[0]
			if (p.Name == nil) != (tt.wantName == "") || (p.Name != nil && *p.Name != tt.wantName) {
				t.Errorf("name = %v, want %q", p.Name, tt.wantName)
			}
			if (p.AvatarURL == nil) != (tt.wantAvatar == "") || (p.AvatarURL != nil && *p.AvatarURL != tt.wantAvatar) {
				t.Errorf("avatar = %v, want %q", p.AvatarURL, tt.wantAvatar)
			}
		})
	}
}
