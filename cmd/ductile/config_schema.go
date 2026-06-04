package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mattjoyce/ductile/internal/configschema"
)

func printConfigSchemaHelp() {
	fmt.Println("Usage: ductile config schema [--name NAME]")
	fmt.Println("Dump an embedded JSON Schema — authoritative, shipped inside the binary")
	fmt.Println("(not read from the on-disk schemas/ files). With no --name, lists names.")
}

// runConfigSchema lists or dumps the JSON Schemas embedded in the binary.
func runConfigSchema(args []string) int {
	fs := flag.NewFlagSet("config schema", flag.ContinueOnError)
	name := fs.String("name", "", "Schema to dump (e.g. config, plugins, tokens); empty lists names")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *name == "" {
		for _, n := range configschema.Names() {
			fmt.Println(n)
		}
		return 0
	}

	data, err := configschema.Bytes(*name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "schema: %v\n", err)
		return 1
	}
	if _, err := os.Stdout.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "schema: write: %v\n", err)
		return 1
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		fmt.Println()
	}
	return 0
}

func printConfigValidateHelp() {
	fmt.Println("Usage: ductile config validate --file PATH [--name NAME] [--whole]")
	fmt.Println("Statically validate a config file against an embedded JSON Schema.")
	fmt.Println("Structure-only: no decryption and no age key required, so it runs")
	fmt.Println("unprivileged. Use 'config check' for a full load + integrity check.")
	fmt.Println("Checks only the named file's structure — not files pulled in via include[].")
	fmt.Println("--name defaults to 'config' (lenient: validates whole files AND include")
	fmt.Println("fragments — every key present is type-checked, but no key is required).")
	fmt.Println("--whole asserts a COMPLETE single-file config (requires service, state,")
	fmt.Println("plugin_roots); use it for a top-level config.yaml, not an include fragment.")
}

// runConfigValidate statically validates a config file against an embedded
// schema. It deliberately does not Load() the config — no decryption, no key —
// so an unprivileged caller can run it even when the gateway runs as root.
func runConfigValidate(args []string) int {
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	file := fs.String("file", "", "Path to the config file to validate")
	name := fs.String("name", "config", "Schema name to validate against")
	whole := fs.Bool("whole", false, "Require a complete config (service, state, plugin_roots) — not an include fragment")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *file == "" {
		fmt.Fprintln(os.Stderr, "validate: --file is required")
		return 1
	}

	// #nosec G304 -- file path is operator-controlled local input.
	data, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate: read %s: %v\n", *file, err)
		return 1
	}

	if canonical, folded := configschema.FoldedInto(*name); folded {
		fmt.Fprintf(os.Stderr, "note: schema %q was folded into the single %q schema (ductile #36); validating against it.\n", *name, canonical)
		*name = canonical
	}

	if *whole {
		if *name != "config" {
			fmt.Fprintln(os.Stderr, "validate: --whole only applies to the 'config' schema")
			return 1
		}
		if err := configschema.ValidateYAMLWhole(data); err != nil {
			fmt.Fprintf(os.Stderr, "INVALID — %s as a whole config:\n%v\n", *file, err)
			return 1
		}
		fmt.Printf("OK: %s is a valid whole config\n", *file)
		return 0
	}

	if err := configschema.ValidateYAML(*name, data); err != nil {
		fmt.Fprintf(os.Stderr, "INVALID — %s against the %q schema:\n%v\n", *file, *name, err)
		return 1
	}
	fmt.Printf("OK: %s is valid against the %q schema\n", *file, *name)
	return 0
}
