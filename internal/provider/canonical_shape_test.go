package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr/xattr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
)

// modelsConfigValidate runs the models_config custom type's ValidateAttribute over
// s and returns its diagnostics (the plan-time check Terraform runs on config).
func modelsConfigValidate(s string) diag.Diagnostics {
	v := modelsConfigFromRaw([]byte(s))
	resp := &xattr.ValidateAttributeResponse{}
	v.ValidateAttribute(context.Background(), xattr.ValidateAttributeRequest{Path: path.Root("models_config")}, resp)
	return resp.Diagnostics
}

func retryConfigValidate(s string) diag.Diagnostics {
	v := retryConfigFromRaw([]byte(s))
	resp := &xattr.ValidateAttributeResponse{}
	v.ValidateAttribute(context.Background(), xattr.ValidateAttributeRequest{Path: path.Root("retry_config")}, resp)
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

// TestModelsConfigShapeValidation proves models_config shape validation accepts
// the object shapes the client decodes into restgen.ModelsConfig and rejects a
// non-object, an unknown key (named in the diagnostic), or a wrong-typed field —
// all at plan time rather than apply.
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

// TestRetryConfigShapeValidation proves retry_config shape validation mirrors
// restgen.PolicyRetryConfig{count int64, on_codes []int64}.
func TestRetryConfigShapeValidation(t *testing.T) {
	valid := []string{
		`{"count":3,"on_codes":[429,503]}`,
		`{"count":3}`,
		`{"count":3,"on_codes":[]}`,   // canon-equivalent to an absent on_codes
		`{"count":3,"on_codes":null}`, // canon-equivalent to an absent on_codes
		`{}`,
	}
	for _, s := range valid {
		if diags := retryConfigValidate(s); diags.HasError() {
			t.Errorf("valid retry_config %s rejected: %v", s, diags)
		}
	}

	t.Run("array rejected", func(t *testing.T) {
		if !retryConfigValidate(`[429,503]`).HasError() {
			t.Error("top-level array must be rejected")
		}
	})
	t.Run("scalar rejected", func(t *testing.T) {
		if !retryConfigValidate(`3`).HasError() {
			t.Error("scalar must be rejected")
		}
	})
	t.Run("unknown key names the key", func(t *testing.T) {
		diags := retryConfigValidate(`{"count":3,"bogus":1}`)
		if !diags.HasError() {
			t.Fatal("unknown key must be rejected")
		}
		if !diagsContain(diags, "bogus") {
			t.Errorf("diagnostic must name the offending key %q: %v", "bogus", diags)
		}
	})
	t.Run("wrong-typed and non-integer fields rejected", func(t *testing.T) {
		for _, s := range []string{
			`{"count":"3"}`,        // count must be a number
			`{"count":3.5}`,        // count must be an integer
			`{"on_codes":{}}`,      // on_codes must be an array
			`{"on_codes":[429.5]}`, // elements must be integers
			`{"on_codes":["429"]}`, // elements must be integers, not strings
		} {
			if !retryConfigValidate(s).HasError() {
				t.Errorf("invalid retry_config %s must be rejected", s)
			}
		}
	})
}

// TestShapeValidationAcceptsCanonEquivalenceInputs guards that adding shape
// validation did NOT regress the canonicalization inputs: every config spelling
// the semantic-equality tests treat as equivalent must still pass validation.
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
	retryInputs := []string{
		`{"count":3,"on_codes":[]}`,
		`{"count":3,"on_codes":null}`,
		`{"count":3}`,
		`{"count":3,"on_codes":[429,503]}`,
	}
	for _, s := range retryInputs {
		if diags := retryConfigValidate(s); diags.HasError() {
			t.Errorf("canon-equivalence retry_config input %s must pass shape validation: %v", s, diags)
		}
	}
}
