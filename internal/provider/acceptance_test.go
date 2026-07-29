package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// testAccProtoV6ProviderFactories wires the in-process provider for the
// acceptance harness. The provider block reads ORQ_URL / ORQ_API_KEY from the env.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"orq": providerserver.NewProtocol6WithError(New("acctest")()),
}

func testAccPreCheck(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ORQ_URL", "ORQ_API_KEY"} {
		if os.Getenv(k) == "" {
			t.Fatalf("%s must be set for TF_ACC acceptance tests", k)
		}
	}
}

// TestAccProjectsDataSource is a real end-to-end read against a live orq stack.
// It runs only under `make testacc` (TF_ACC=1); resource.Test skips otherwise.
func TestAccProjectsDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Empty provider block => url/api_key resolve from ORQ_URL/ORQ_API_KEY.
				Config: `
provider "orq" {}

data "orq_projects" "all" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					// The list must be present (>= 0 elements) — proves the
					// Connect read + pagination round-tripped without error.
					resource.TestCheckResourceAttrSet("data.orq_projects.all", "projects.#"),
				),
			},
		},
	})
}
