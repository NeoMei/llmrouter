// Minimal non-billing product consumer. Supply only a local test bearer.
package main

import (
	"context"
	"fmt"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"log"
	"os"
	"sync"
)

type localStore struct {
	sync.Mutex
	users map[string]userkit.LocalUser
}

func (s *localStore) Ensure(_ context.Context, a userkit.Access) (userkit.LocalUser, error) {
	s.Lock()
	defer s.Unlock()
	key := a.Issuer + "|" + a.Subject
	if u, ok := s.users[key]; ok {
		return u, nil
	}
	u := userkit.LocalUser{ID: fmt.Sprintf("notes-%d", len(s.users)+1)}
	s.users[key] = u
	return u, nil
}
func main() {
	client, e := userkit.NewClient(userkit.Config{BaseURL: os.Getenv("UNIHUB_API"), Issuer: os.Getenv("UNIHUB_ISSUER"), ProductID: os.Getenv("PRODUCT_ID"), ClientID: os.Getenv("CLIENT_ID")})
	if e != nil {
		log.Fatal("invalid product configuration")
	}
	service, e := userkit.NewService(client, &localStore{users: map[string]userkit.LocalUser{}}, nil)
	if e != nil {
		log.Fatal(e)
	}
	p, e := service.Authenticate(context.Background(), os.Getenv("PRODUCT_BEARER"))
	if e != nil {
		log.Fatal("product access denied")
	}
	if e = p.RequirePermission(context.Background(), "product:use", ""); e != nil {
		log.Fatal("product use denied")
	}
	fmt.Printf("local user %s; current roles %v\n", p.LocalUser.ID, p.Access.Roles)
}
