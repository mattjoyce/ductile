package config

// Hand-built parity cases: one per Problem-now bug and per decision-note item
// the static corpus does not reach (card 153 slice 3). Each id names the item.

// parityBase is a minimal config both loaders accept in full mode.
const parityBase = `service:
  name: parity
state:
  path: ./state.db
plugin_roots:
  - ./plugins
api:
  enabled: false
`

func parityRoot(extra string) string { return parityBase + extra }

func parityHandBuiltCases() []parityCase {
	var cases []parityCase
	cases = append(cases, parityMergeCases()...)
	cases = append(cases, parityIncludeCases()...)
	cases = append(cases, parityEnvCases()...)
	cases = append(cases, paritySyntaxCases()...)
	cases = append(cases, parityPostureSplitCases()...)
	return cases
}

// parityMergeCases cover the include merge (Problem now; decision I).
func parityMergeCases() []parityCase {
	return []parityCase{
		{
			id: "merge/include-turns-api-off",
			files: map[string]string{
				"config.yaml": `service:
  name: parity
state:
  path: ./state.db
plugin_roots:
  - ./plugins
api:
  enabled: true
  listen: "127.0.0.1:18799"
include:
  - api.yaml
`,
				"api.yaml": "api:\n  enabled: false\n",
			},
		},
		{
			id: "merge/include-dropped-keys",
			files: map[string]string{
				"config.yaml": parityRoot("include:\n  - extra.yaml\n"),
				"extra.yaml": `service:
  max_workers: 3
  strict_mode: true
  allow_symlinks: true
  hook_max_depth: 6
  unconfined: true
  plugin_env_passthrough: [PARITY_PASS]
  admission:
    verify_integrity_on_boot: true
    fail_on_drift: true
accounts:
  default:
    uid: 4181
    gid: 4181
    state_dir: __CASE_DIR__/accounts/default
secrets:
  vault_file: other-vault.age
tcc_paths:
  - __CASE_DIR__/tcc
database:
  path: ./other.db
`,
			},
		},
		{
			id: "merge/include-false-replaces-true",
			files: map[string]string{
				"config.yaml": parityRoot(`remote_ingress:
  listen_path: /relay
  require_key_id: true
include:
  - ingress.yaml
`),
				"ingress.yaml": "remote_ingress:\n  require_key_id: false\n",
			},
		},
		{
			id: "merge/plugins-entry-replaced-whole",
			files: map[string]string{
				"config.yaml": parityRoot(`plugins:
  echo:
    enabled: true
    parallelism: 1
    config:
      a: 1
include:
  - plugins.yaml
`),
				"plugins.yaml": "plugins:\n  echo:\n    enabled: true\n    config:\n      b: 2\n",
			},
		},
		{
			id: "merge/routes-three-files-in-order",
			files: map[string]string{
				"config.yaml": parityRoot(`routes:
  - {from: a, event_type: one, to: b}
include:
  - r2.yaml
  - r3.yaml
`),
				"r2.yaml": "routes:\n  - {from: a, event_type: two, to: b}\n",
				"r3.yaml": "routes:\n  - {from: a, event_type: three, to: b}\n",
			},
		},
		{
			id: "merge/lists-append-across-files",
			files: map[string]string{
				"config.yaml": parityRoot(`api:
  enabled: false
  allowed_origins: [https://a.example]
include:
  - more.yaml
`),
				"more.yaml": `plugin_roots:
  - ./more-plugins
api:
  allowed_origins: [https://b.example]
`,
			},
		},
		{
			id: "merge/null-plugins-in-include",
			files: map[string]string{
				"config.yaml": parityRoot("plugins:\n  echo:\n    enabled: false\ninclude:\n  - null.yaml\n"),
				"null.yaml":   "plugins:\n",
			},
		},
		{
			id: "merge/empty-collections-in-include",
			files: map[string]string{
				"config.yaml": parityRoot("plugins:\n  echo:\n    enabled: false\nroutes:\n  - {from: a, event_type: x, to: b}\ninclude:\n  - empty.yaml\n"),
				"empty.yaml":  "plugins: {}\nroutes: []\n",
			},
		},
		{
			id: "merge/timeouts-inline-overrides",
			files: map[string]string{
				"config.yaml": parityRoot(`plugins:
  echo:
    enabled: false
    timeouts:
      poll: 5s
      cpu: 15s
`),
			},
		},
	}
}

// parityIncludeCases cover include traversal (D, G empty file, H, cycles).
func parityIncludeCases() []parityCase {
	return []parityCase{
		{
			id: "include/absolute-path",
			files: map[string]string{
				"config.yaml":         parityRoot("include:\n  - __CASE_DIR__/shared/plugins.yaml\n"),
				"shared/plugins.yaml": "plugins:\n  echo:\n    enabled: false\n",
			},
		},
		{
			id:   "include/parent-relative",
			root: "etc/config.yaml",
			files: map[string]string{
				"etc/config.yaml":     parityRoot("include:\n  - ../shared/plugins.yaml\n"),
				"shared/plugins.yaml": "plugins:\n  echo:\n    enabled: false\n",
			},
		},
		{
			id: "include/nested-chain",
			files: map[string]string{
				"config.yaml": parityRoot("include:\n  - a.yaml\n"),
				"a.yaml":      "include:\n  - b.yaml\nroutes:\n  - {from: a, event_type: one, to: b}\n",
				"b.yaml":      "routes:\n  - {from: b, event_type: two, to: c}\n",
			},
		},
		{
			id: "include/directory",
			files: map[string]string{
				"config.yaml":          parityRoot("include:\n  - conf.d\n"),
				"conf.d/a.yaml":        "routes:\n  - {from: a, event_type: yaml, to: b}\n",
				"conf.d/b.yml":         "routes:\n  - {from: a, event_type: yml, to: b}\n",
				"conf.d/UPPER.YAML":    "routes:\n  - {from: a, event_type: upper, to: b}\n",
				"conf.d/nested/c.yaml": "routes:\n  - {from: a, event_type: nested, to: b}\n",
				"conf.d/ignored.txt":   "not yaml\n",
			},
		},
		{
			id: "include/cycle",
			files: map[string]string{
				"config.yaml": parityRoot("include:\n  - a.yaml\n"),
				"a.yaml":      "include:\n  - b.yaml\n",
				"b.yaml":      "include:\n  - a.yaml\n",
			},
		},
		{
			id: "include/empty-file",
			files: map[string]string{
				"config.yaml": parityRoot("include:\n  - empty.yaml\n"),
				"empty.yaml":  "",
			},
		},
		{
			id:      "include/age-encrypted",
			encrypt: []string{"plugins.yaml"},
			files: map[string]string{
				"config.yaml":  parityRoot("include:\n  - plugins.yaml\n"),
				"plugins.yaml": "plugins:\n  echo:\n    enabled: false\n",
			},
		},
	}
}

// parityEnvCases cover interpolation and dotenv loading (Problem now; E, F, G,
// and the empty-value prerequisite).
func parityEnvCases() []parityCase {
	return []parityCase{
		{
			id:    "env/unresolved-variable",
			files: map[string]string{"config.yaml": parityRoot("tcc_paths:\n  - ${PARITY_MISSING}/x\n")},
		},
		{
			id:    "env/value-with-colon-space",
			env:   map[string]string{"PARITY_COLON": "a: b"},
			files: map[string]string{"config.yaml": parityRoot("tcc_paths:\n  - ${PARITY_COLON}\n")},
		},
		{
			id:    "env/set-but-empty",
			env:   map[string]string{"PARITY_EMPTY": ""},
			files: map[string]string{"config.yaml": parityRoot("tcc_paths:\n  - x${PARITY_EMPTY}\n")},
		},
		{
			id:  "env/variable-in-include-path",
			env: map[string]string{"PARITY_DIR": "sub"},
			files: map[string]string{
				"config.yaml":      parityRoot("include:\n  - ${PARITY_DIR}/plugins.yaml\n"),
				"sub/plugins.yaml": "plugins:\n  echo:\n    enabled: false\n",
			},
		},
		{
			id: "env/dotenv-from-later-file",
			files: map[string]string{
				"config.yaml": parityRoot("tcc_paths:\n  - ${PARITY_LATE}\ninclude:\n  - extra.yaml\n"),
				"extra.yaml":  "environment_vars:\n  include:\n    - late.env\n",
				"late.env":    "PARITY_LATE=from-late-file\n",
			},
		},
		{
			id: "env/dotenv-duplicate-name",
			files: map[string]string{
				"config.yaml": parityRoot("environment_vars:\n  include:\n    - dup.env\ntcc_paths:\n  - ${PARITY_DUP}\n"),
				"dup.env":     "PARITY_DUP=first\nPARITY_DUP=second\n",
			},
		},
		{
			id:  "env/process-wins-over-dotenv",
			env: map[string]string{"PARITY_PROC": "process"},
			files: map[string]string{
				"config.yaml": parityRoot("environment_vars:\n  include:\n    - proc.env\ntcc_paths:\n  - ${PARITY_PROC}\n"),
				"proc.env":    "PARITY_PROC=file\nPARITY_PROC_ONLY=file-only\n",
			},
		},
		{
			id: "env/dotenv-dashed-name",
			files: map[string]string{
				"config.yaml": parityRoot("environment_vars:\n  include:\n    - dashed.env\n"),
				"dashed.env":  "PARITY-DASHED=x\n",
			},
		},
	}
}

// paritySyntaxCases cover the YAML tightenings (G) and the spellings Ashlar's
// reader refuses that G does not list.
func paritySyntaxCases() []parityCase {
	return []parityCase{
		{
			id: "syntax/bare-integer-duration",
			files: map[string]string{"config.yaml": `service:
  name: parity
  tick_interval: 60
state:
  path: ./state.db
plugin_roots:
  - ./plugins
api:
  enabled: false
`},
		},
		{
			id: "syntax/anchor-and-alias",
			files: map[string]string{"config.yaml": parityRoot(`plugins:
  one: &shared
    enabled: false
  two: *shared
`)},
		},
		{
			id:    "syntax/leading-zero-integer",
			files: map[string]string{"config.yaml": parityRoot("plugins:\n  echo:\n    enabled: false\n    parallelism: 010\n")},
		},
		{
			id:    "syntax/yaml11-boolean",
			files: map[string]string{"config.yaml": parityRoot("plugins:\n  echo:\n    enabled: yes\n")},
		},
		{
			id:    "syntax/second-document",
			files: map[string]string{"config.yaml": parityRoot("---\nservice:\n  name: second\n")},
		},
		{
			id:    "syntax/unknown-key",
			files: map[string]string{"config.yaml": parityRoot("log_level: info\nservice_typo: true\n")},
		},
	}
}

// parityPostureSplitCases are the posture shapes with the posture-bearing key
// moved into an include, where load() drops it today.
func parityPostureSplitCases() []parityCase {
	return []parityCase{
		{
			id: "posture/accounts-in-include",
			files: map[string]string{
				"config.yaml": parityRoot("include:\n  - accounts.yaml\n"),
				"accounts.yaml": `accounts:
  default:
    uid: 4181
    gid: 4181
    state_dir: __CASE_DIR__/accounts/default
  untrusted:
    uid: 4182
    gid: 4182
    state_dir: __CASE_DIR__/accounts/untrusted
`,
			},
		},
		{
			id: "posture/unconfined-in-include",
			files: map[string]string{
				"config.yaml":  parityRoot("include:\n  - service.yaml\n"),
				"service.yaml": "service:\n  unconfined: true\n",
			},
		},
	}
}
