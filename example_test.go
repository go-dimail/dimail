package dimail_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/go-dimail/dimail"
)

// This example mirrors the README. It is compiled (guaranteeing the documented
// API stays valid) but not run, since it would contact the live service.
func Example() {
	ctx := context.Background()

	c := dimail.NewClient(dimail.WithBasicAuth("apiuser", "apipass"))
	if _, err := c.Login(ctx); err != nil { // fetches and stores a bearer token
		log.Fatal(err)
	}

	domains, err := c.GetDomains(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range domains {
		fmt.Printf("%s (state=%s)\n", d.Name, d.State)
	}

	mb, err := c.PostMailboxV2(ctx, "example.gouv.fr", "jean.dupont", &dimail.CreateMailbox2{
		Features: []dimail.MailboxFeature{dimail.MailboxFeatureOX},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("created:", mb.Email)
}

// ExampleAPIError_NotFound shows the typed-error surface.
func ExampleAPIError_notFound() {
	c := dimail.NewClient(dimail.WithToken(os.Getenv("DIMAIL_TOKEN")))

	_, err := c.GetDomain(context.Background(), "absent.example")
	var apiErr *dimail.APIError
	if errors.As(err, &apiErr) && apiErr.NotFound() {
		fmt.Println("no such domain")
	}
}
