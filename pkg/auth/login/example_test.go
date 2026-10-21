package login_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
)

func Example() {
	// The hash is what `agentkit users hash-password` prints; it goes in the
	// config's auth.users.
	hash, err := login.HashPassword("correct horse battery staple")
	if err != nil {
		panic(err)
	}
	l, err := login.New([]login.User{{Username: "ada", PasswordHash: hash, Tenant: "acme", Roles: []string{login.RoleAdmin}}})
	if err != nil {
		panic(err)
	}

	// POST /v1/auth/login: the cookie goes on the response.
	signIn := httptest.NewRequest(http.MethodPost, "https://agents.example.com/v1/auth/login", nil)
	if _, err := l.SignIn(signIn, "ada", "wrong"); errors.Is(err, login.ErrBadLogin) {
		fmt.Println("wrong password refused")
	}
	cookie, err := l.SignIn(signIn, "ada", "correct horse battery staple")
	if err != nil {
		panic(err)
	}
	fmt.Println(cookie.Name, cookie.HttpOnly, cookie.Secure, cookie.SameSite == http.SameSiteStrictMode)

	// Later requests carry the cookie; Login is one authenticator of a
	// chain, after the bearer ones.
	authn := auth.Chain(l)
	r := httptest.NewRequest(http.MethodGet, "https://agents.example.com/v1/agents", nil)
	r.AddCookie(cookie)
	p, err := authn.Authenticate(r)
	if err != nil {
		panic(err)
	}
	fmt.Println(p.User, p.Tenant.ID(), p.Via.Kind, p.HasScope("admin:agents"))

	// An unsafe request also needs the Origin and the CSRF token.
	post := httptest.NewRequest(http.MethodPost, "https://agents.example.com/v1/runs", nil)
	post.AddCookie(cookie)
	fmt.Println(errors.Is(l.CheckCSRF(post), login.ErrCSRF))
	info, _ := l.Session(post)
	post.Header.Set("Origin", "https://agents.example.com")
	post.Header.Set(login.CSRFHeader, info.CSRF)
	fmt.Println(l.CheckCSRF(post))
	// Output:
	// wrong password refused
	// __Host-agentkit_session true true true
	// ada acme session true
	// true
	// <nil>
}
