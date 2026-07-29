package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr/xattr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
)

// modelsConfigValidate is the plan-time check Terraform runs on config.
func modelsConfigValidate(s string) diag.Diagnostics {
	v := modelsConfigFromRaw([]byte(s))
	resp := &xattr.ValidateAttributeResponse{}
	v.ValidateAttribute(context.Background(), xattr.ValidateAttributeRequest{Path: path.Root("models_config")}, resp)
	return resp.Diagnostics
}

// diagsContain reports whether any diagnostic's summary or detail contains sub.
func diagsContain(diags diag.Diagnostics, sub string) bool {
	for _, d := range diags {
		if strings.Contains(d.Summary(), sub) || strings.Contains(d.Detail(), sub) {
			return true
		}
	}
	return false
}

func TestModelsConfigShapeValidation(t *testing.T) {
	valid := []string{
		`{"mode":"fallback","models":[{"model":"x","weight":0.5}]}`,
		`{"models":[{"model":"x"}]}`,         // weight-less: canon-equivalent, must pass
		`{"mode":"round_robin","models":[]}`, // empty models array
		`{"models":null}`,                    // Models is a nullable pointer
		`{}`,                                 // no keys is a valid (if empty) object
		`{"models":[{"model":"x","display_name":"X","integration_id":"i","weight":1}]}`, // all ModelRef fields
	}
	for _, s := range valid {
		if diags := modelsConfigValidate(s); diags.HasError() {
			t.Errorf("valid models_config %s rejected: %v", s, diags)
		}
	}

	t.Run("array rejected", func(t *testing.T) {
		if !modelsConfigValidate(`[{"model":"x"}]`).HasError() {
			t.Error("top-level array must be rejected")
		}
	})
	t.Run("scalar number rejected", func(t *testing.T) {
		if !modelsConfigValidate(`5`).HasError() {
			t.Error("scalar number must be rejected")
		}
	})
	t.Run("scalar string rejected", func(t *testing.T) {
		if !modelsConfigValidate(`"hello"`).HasError() {
			t.Error("scalar string must be rejected")
		}
	})
	t.Run("unknown top-level key names the key", func(t *testing.T) {
		diags := modelsConfigValidate(`{"mode":"fallback","foo":1}`)
		if !diags.HasError() {
			t.Fatal("unknown key must be rejected")
		}
		if !diagsContain(diags, "foo") {
			t.Errorf("diagnostic must name the offending key %q: %v", "foo", diags)
		}
	})
	t.Run("unknown model-entry key names the key", func(t *testing.T) {
		diags := modelsConfigValidate(`{"models":[{"model":"x","wieght":0.5}]}`)
		if !diags.HasError() {
			t.Fatal("unknown model-entry key must be rejected")
		}
		if !diagsContain(diags, "wieght") {
			t.Errorf("diagnostic must name the offending key %q: %v", "wieght", diags)
		}
	})
	t.Run("wrong-typed known fields rejected", func(t *testing.T) {
		for _, s := range []string{
			`{"mode":5}`,               // mode must be a string
			`{"models":{}}`,            // models must be an array
			`{"models":["x"]}`,         // model entry must be an object
			`{"models":[{"model":5}]}`, // model must be a string
			`{"models":[{"model":"x","weight":"heavy"}]}`, // weight must be a number
		} {
			if !modelsConfigValidate(s).HasError() {
				t.Errorf("wrong-typed models_config %s must be rejected", s)
			}
		}
	})
}

// Every config spelling the semantic-equality tests treat as equivalent must
// still pass shape validation.
func TestShapeValidationAcceptsCanonEquivalenceInputs(t *testing.T) {
	modelsInputs := []string{
		`{"mode":"fallback","models":[{"model":"x"},{"model":"y","weight":0.25}]}`,
		`{"mode":"fallback","models":[{"model":"x","weight":0.5},{"model":"y","weight":0.25}]}`,
		`{"models":[{"model":"x","weight":0}]}`,
		`{"models":[{"model":"x","weight":0.50}]}`,
		`{"models":[{"model":"x","weight":0.500}]}`,
		`{"models":[{"model":"x","weight":5e-1}]}`,
		`{"models":[{"model":"x","weight":1}]}`,
		`{"models":[{"model":"x","weight":1.00}]}`,
	}
	for _, s := range modelsInputs {
		if diags := modelsConfigValidate(s); diags.HasError() {
			t.Errorf("canon-equivalence models_config input %s must pass shape validation: %v", s, diags)
		}
	}
}

// JSON null decodes to the Go zero value for the NON-pointer restgen fields,
// which the server then rejects at apply.
func TestShapeValidationRejectsNullForNonNullableScalars(t *testing.T) {
	t.Run("mode null rejected", func(t *testing.T) {
		diags := modelsConfigValidate(`{"mode":null}`)
		if !diags.HasError() {
			t.Fatal("mode:null must be rejected at plan time")
		}
		if !diagsContain(diags, `"mode" must be a string, got null`) {
			t.Errorf("diagnostic must name mode/null: %v", diags)
		}
	})
	t.Run("model null rejected", func(t *testing.T) {
		diags := modelsConfigValidate(`{"models":[{"model":null}]}`)
		if !diags.HasError() {
			t.Fatal("model:null must be rejected at plan time")
		}
		if !diagsContain(diags, `"model" must be a string, got null`) {
			t.Errorf("diagnostic must name model/null: %v", diags)
		}
	})

	// Nullable pointer fields: null stays accepted.
	for _, tc := range []struct{ name, cfg string }{
		{"weight null", `{"models":[{"model":"x","weight":null}]}`},
		{"display_name null", `{"models":[{"model":"x","display_name":null}]}`},
		{"integration_id null", `{"models":[{"model":"x","integration_id":null}]}`},
	} {
		t.Run(tc.name+" accepted", func(t *testing.T) {
			if diags := modelsConfigValidate(tc.cfg); diags.HasError() {
				t.Errorf("nullable-field null must stay accepted, got: %v", diags)
			}
		})
	}
}
