// Package contracts embeds the JSON Schemas shared with the Cloudflare Worker
// and validates documents against them from Go.
//
// The Worker imports the same three files, and both runtimes check every file
// in fixtures/ with the same expectations (contracts/README.md). That is what
// stops the two languages drifting apart unnoticed (docs/plans/testing.md §2).
package contracts

import (
	"bytes"
	"embed"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed save.schema.json upload.schema.json job.schema.json
var schemaFiles embed.FS

// Schema names, as a fixture's "schema" field spells them.
const (
	Save   = "save"
	Upload = "upload"
	Job    = "job"
)

var names = []string{Save, Upload, Job}

var (
	compileOnce sync.Once
	compiled    map[string]*jsonschema.Schema
	compileErr  error
)

func compile() {
	compiler := jsonschema.NewCompiler()
	compiled = make(map[string]*jsonschema.Schema)
	for _, name := range names {
		file := name + ".schema.json"
		raw, err := schemaFiles.ReadFile(file)
		if err != nil {
			compileErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			compileErr = fmt.Errorf("contracts: parse %s: %w", file, err)
			return
		}
		if err := compiler.AddResource(file, doc); err != nil {
			compileErr = fmt.Errorf("contracts: add %s: %w", file, err)
			return
		}
	}
	for _, name := range names {
		schema, err := compiler.Compile(name + ".schema.json")
		if err != nil {
			compileErr = fmt.Errorf("contracts: compile %s: %w", name, err)
			return
		}
		compiled[name] = schema
	}
}

// Validate checks doc against the named schema. doc is what encoding/json or
// jsonschema.UnmarshalJSON produced (maps, slices, strings, numbers, bools,
// nil). A nil error means the document satisfies the contract.
func Validate(name string, doc any) error {
	compileOnce.Do(compile)
	if compileErr != nil {
		return compileErr
	}
	schema, ok := compiled[name]
	if !ok {
		return fmt.Errorf("contracts: unknown schema %q", name)
	}
	return schema.Validate(doc)
}
