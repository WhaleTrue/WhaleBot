package agent

import (
	"bytes"
	"encoding/json"
	"fmt"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type localSchemaLoader struct{}

func (localSchemaLoader) Load(location string) (any, error) {
	return nil, fmt.Errorf("external schema reference forbidden: %s", location)
}
func validateArguments(schema, arguments json.RawMessage) error {
	var definition, value any
	if err := json.Unmarshal(schema, &definition); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(localSchemaLoader{})
	const location = "https://whalemesh.dev/agent-tool.schema.json"
	if err := compiler.AddResource(location, definition); err != nil {
		return err
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return err
	}
	if err = compiled.Validate(value); err != nil {
		return fmt.Errorf("tool arguments fail exported schema: %w", err)
	}
	return nil
}
