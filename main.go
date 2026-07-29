// terraform-provider-orq is a Terraform provider for orq.ai workspace resources.
//
// Local development status: NOT published to any registry yet. The upstream
// GitHub repo orq-ai/terraform-provider-orq is created separately by the repo
// owner. See README.md.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/orq-ai/terraform-provider-orq/internal/provider"
)

// These are set by goreleaser at build/publish time (see .goreleaser.yml).
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		// registry.terraform.io / OpenTofu registry require this exact address
		// form: <namespace>/<type>. Must match the published repo name
		// terraform-provider-<type> (orq-ai/terraform-provider-orq -> orq-ai/orq).
		Address: "registry.terraform.io/orq-ai/orq",
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err.Error())
	}
}
