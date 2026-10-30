// Command specgen prints the OpenAPI 3.1 contract generated from the Huma
// operations. `make spec` writes it to api/openapi.yaml; `make check-spec`
// compares a fresh run with the committed file.
package main

import (
	"fmt"
	"os"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
)

func main() {
	out, err := httpapi.Spec()
	if err != nil {
		fmt.Fprintln(os.Stderr, "specgen:", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintln(os.Stderr, "specgen:", err)
		os.Exit(1)
	}
}
