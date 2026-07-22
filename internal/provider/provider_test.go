package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestProjectsDataSourceRegistered is the wiring smoke test: the provider
// registers orq_projects, its metadata resolves to the expected type name, and
// its schema builds without diagnostics. (A live end-to-end read requires
// ORQ_URL/ORQ_API_KEY and runs under `make testacc`.)
func TestProjectsDataSourceRegistered(t *testing.T) {
	ctx := context.Background()
	p := New("test")()

	factories := p.DataSources(ctx)
	if len(factories) != 1 {
		t.Fatalf("expected 1 data source, got %d", len(factories))
	}

	ds := factories[0]()

	var meta datasource.MetadataResponse
	ds.Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "orq"}, &meta)
	if meta.TypeName != "orq_projects" {
		t.Fatalf("data source type name = %q, want orq_projects", meta.TypeName)
	}

	var sch datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &sch)
	if sch.Diagnostics.HasError() {
		t.Fatalf("orq_projects schema has errors: %v", sch.Diagnostics)
	}
	if _, ok := sch.Schema.Attributes["projects"]; !ok {
		t.Fatal("orq_projects schema missing 'projects' attribute")
	}
}

// TestResolveString covers the credential/URL resolution precedence:
// explicit attribute > environment variable > fallback.
func TestResolveString(t *testing.T) {
	const envKey = "ORQ_TEST_TOKEN"

	tests := []struct {
		name     string
		attr     types.String
		env      string // "" means unset
		fallback string
		want     string
	}{
		{
			name: "attr beats env",
			attr: types.StringValue("attr-token"),
			env:  "env-token",
			want: "attr-token",
		},
		{
			name: "env used when attr null",
			attr: types.StringNull(),
			env:  "env-token",
			want: "env-token",
		},
		{
			name: "attr beats env even when env set and attr empty-string",
			attr: types.StringValue(""),
			env:  "env-token",
			want: "", // explicit empty attribute is still "set" and wins
		},
		{
			name:     "fallback when attr null and env unset",
			attr:     types.StringNull(),
			env:      "",
			fallback: "https://my.orq.ai",
			want:     "https://my.orq.ai",
		},
		{
			name: "unknown attr treated as unset, env wins",
			attr: types.StringUnknown(),
			env:  "env-token",
			want: "env-token",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv(envKey, tc.env)
			} else {
				// Ensure a stale value from the process env can't leak in.
				t.Setenv(envKey, "")
			}
			got := resolveString(tc.attr, envKey, tc.fallback)
			if got != tc.want {
				t.Fatalf("resolveString() = %q, want %q", got, tc.want)
			}
		})
	}
}
