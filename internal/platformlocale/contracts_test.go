package platformlocale

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestOpenAPIContractsUseApprovedLocaleEnum(t *testing.T) {
	t.Parallel()
	want := All()
	for _, contract := range []struct {
		path string
		name string
	}{
		{path: "../../api/openapi/configuration.openapi.json", name: "configuration"},
		{path: "../../api/openapi/identity.openapi.json", name: "identity"},
	} {
		contents, err := os.ReadFile(contract.path)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(contents, &document); err != nil {
			t.Fatal(err)
		}
		schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
		values := schemas["Locale"].(map[string]any)["enum"].([]any)
		got := make([]string, len(values))
		for index, value := range values {
			got[index] = value.(string)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s locale enum = %v, want %v", contract.name, got, want)
		}
	}
}
