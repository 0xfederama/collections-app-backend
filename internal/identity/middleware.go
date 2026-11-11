package identity

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/users"
)

const (
	headerAccessJWT = "Cf-Access-Jwt-Assertion"
	headerDevUser   = "X-Dev-User"
	cookieDevUser   = "dev_user"
	apiPrefix       = "/api/v1/"
)

// edgeHeaders are added by Cloudflare's edge. A request that carries one did not come from the LAN.
var edgeHeaders = []string{"Cf-Ray", "Cf-Connecting-Ip"}

// Users is what the resolver needs from the accounts: the users service.
type Users interface {
	EnsureByEmail(ctx context.Context, email string) (users.User, error)
	ProfileDue(u users.User) bool
	SaveProfile(ctx context.Context, id uuid.UUID, p users.Profile) error
}

// Options configure a Resolver.
type Options struct {
	// TeamDomain (host, no scheme) and Audience enable Access token verification; both or neither.
	TeamDomain, Audience string
	// DevIdentities, when non-empty, enables the dev identity provider; the first is the default.
	DevIdentities []string
	// LANOwnerEmail, when non-empty, enables the LAN owner fallback.
	LANOwnerEmail string
	Users         Users
	Logger        *slog.Logger
	Clock         clock.Clock
	// HTTPClient serves the key fetch and the profile fetch. Nil uses a client with timeouts.
	HTTPClient *http.Client
}

// Resolver is the identity middleware of /api/v1.
type Resolver struct {
	o        Options
	verifier *oidc.IDTokenVerifier // nil without Access config
	client   *http.Client
	profiles sync.WaitGroup
}

// New builds a Resolver. The issuer is https://<TeamDomain>; the audience must match Audience.
func New(o Options) *Resolver {
	if o.Clock == nil {
		o.Clock = clock.System{}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	r := &Resolver{o: o, client: client}
	if o.TeamDomain != "" && o.Audience != "" {
		keys := NewKeySet(o.TeamDomain, client, o.Clock)
		r.verifier = oidc.NewVerifier("https://"+o.TeamDomain, keys, &oidc.Config{
			ClientID:             o.Audience,
			SupportedSigningAlgs: []string{"RS256"},
			Now:                  o.Clock.Now,
		})
	}
	return r
}

// Wait blocks until the background profile fetches started so far are done (tests).
func (r *Resolver) Wait() { r.profiles.Wait() }

// Middleware resolves the caller of every /api/v1 request, in this order: (1) a
// Cf-Access-Jwt-Assertion header is verified and nothing else is tried; (2) the dev identity;
// (3) the LAN owner fallback, only without a Cloudflare edge header; (4) 401. The plain
// Cf-Access-Authenticated-User-Email header is never read. Probes and /admin are not touched.
func (r *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasPrefix(req.URL.Path, apiPrefix) {
			next.ServeHTTP(w, req)
			return
		}
		email, token, ok := r.resolveEmail(req)
		if !ok {
			httpserver.Unauthenticated(w, req)
			return
		}
		u, err := r.o.Users.EnsureByEmail(req.Context(), email)
		if err != nil {
			p, unexpected := problem.From(err, problem.RequestIDFrom(req.Context()))
			if unexpected {
				r.o.Logger.ErrorContext(req.Context(), "resolve user", "err", err, "request_id", p.RequestID)
			}
			httpserver.WriteProblem(w, p)
			return
		}
		if token != "" && r.o.Users.ProfileDue(u) {
			r.refreshProfile(req.Context(), u, token)
		}
		next.ServeHTTP(w, req.WithContext(scope.WithUser(req.Context(), u.ID)))
	})
}

// resolveEmail applies the identity order. token is set only for a verified Access token.
func (r *Resolver) resolveEmail(req *http.Request) (email, token string, ok bool) {
	if vals, present := req.Header[headerAccessJWT]; present {
		if r.verifier == nil || len(vals) != 1 {
			return "", "", false
		}
		email, ok = r.verify(req.Context(), vals[0])
		return email, vals[0], ok
	}
	if len(r.o.DevIdentities) > 0 {
		email, ok = r.devIdentity(req)
		return email, "", ok
	}
	if r.o.LANOwnerEmail != "" && !hasEdgeHeader(req) {
		return r.o.LANOwnerEmail, "", true
	}
	return "", "", false
}

// verify checks the token and returns its non-empty email claim.
func (r *Resolver) verify(ctx context.Context, raw string) (string, bool) {
	tok, err := r.verifier.Verify(ctx, raw)
	if err != nil {
		r.o.Logger.DebugContext(ctx, "access token rejected", "err", err) // the error never contains the token
		return "", false
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := tok.Claims(&claims); err != nil || strings.TrimSpace(claims.Email) == "" {
		r.o.Logger.DebugContext(ctx, "access token without an email claim")
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(claims.Email)), true
}

// devIdentity reads X-Dev-User, else the dev_user cookie; nothing means the first identity.
func (r *Resolver) devIdentity(req *http.Request) (string, bool) {
	want := ""
	if vals, present := req.Header[http.CanonicalHeaderKey(headerDevUser)]; present {
		if len(vals) != 1 {
			return "", false
		}
		want = vals[0]
	} else if c, err := req.Cookie(cookieDevUser); err == nil {
		want = c.Value
		if dec, err := url.QueryUnescape(want); err == nil {
			want = dec
		}
	} else {
		return r.o.DevIdentities[0], true
	}
	for _, id := range r.o.DevIdentities {
		if want == id {
			return id, true
		}
	}
	return "", false
}

func hasEdgeHeader(req *http.Request) bool {
	for _, h := range edgeHeaders {
		if _, present := req.Header[h]; present {
			return true
		}
	}
	return false
}

// refreshProfile fetches name and avatar in the background, so a slow Access endpoint never
// delays the request. It has its own timeout, keeps the request's scope values and never fails
// anything: a failure is logged at debug level and retried later.
func (r *Resolver) refreshProfile(reqCtx context.Context, u users.User, token string) {
	ctx := context.WithoutCancel(reqCtx)
	r.profiles.Go(func() {
		p, err := fetchProfile(ctx, r.client, r.o.TeamDomain, token)
		if err != nil {
			r.o.Logger.DebugContext(ctx, "profile fetch failed", "err", err)
			return
		}
		if err := r.o.Users.SaveProfile(ctx, u.ID, p); err != nil {
			r.o.Logger.WarnContext(ctx, "profile save failed", "err", err)
		}
	})
}
