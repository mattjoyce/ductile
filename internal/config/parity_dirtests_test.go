package config

// The multi-file directory tests in loader_test.go, rebuilt whole as parity
// cases (card 153 slice 3 fix round). Lifted out as single literals they stop
// at the missing include in both loaders and compare nothing; here each test's
// directory, vault secrets and .checksums are rebuilt so both loaders get as
// far as the test does. Each id names the test it comes from.

const dirtestBase = `service:
  tick_interval: 60s
  log_level: info
state:
  path: ./test.db
plugin_roots:
  - ./plugins
`

const dirtestSecretsBlock = `secrets:
  age_key_file: age.key
  vault_file: vault.age
`

func parityDirectoryTestCases() []parityCase {
	return []parityCase{
		{
			id: "dirtest/TestLoadMultiFileWithInclude",
			files: map[string]string{
				"config.yaml":  "include:\n  - plugins.yaml\n" + dirtestBase,
				"plugins.yaml": "plugins:\n  echo:\n    enabled: true\n    schedules:\n      - every: 5m\n        jitter: 30s\n",
			},
		},
		{
			id: "dirtest/TestDeepMergeConfigAPIFields",
			files: map[string]string{
				"config.yaml": "include:\n  - api.yaml\n" + dirtestBase,
				"api.yaml": `api:
  enabled: true
  listen: "127.0.0.1:18181"
  auth:
    tokens:
      - secret_ref: api-admin
        scopes: ["*"]
  max_concurrent_sync: 7
  max_sync_timeout: 42s
  allowed_origins: [https://dash.example]
  management_socket: /tmp/ductile-mgmt.sock
`,
			},
		},
		{
			id: "dirtest/TestLoadIncludeDirectory",
			files: map[string]string{
				"config.yaml":     "include:\n  - includes\n" + dirtestBase,
				"includes/a.yaml": "plugins:\n  alpha:\n    enabled: true\n",
				"includes/b.yaml": "plugins:\n  beta:\n    enabled: true\n",
			},
		},
		{
			id: "dirtest/TestLoadEnvironmentVarsInclude",
			files: map[string]string{
				".env":        "DUCTILE_TEST_NAME=envtest\n",
				"config.yaml": "environment_vars:\n  include:\n    - .env\nservice:\n  name: ${DUCTILE_TEST_NAME}\n  tick_interval: 60s\nstate:\n  path: ./test.db\nplugin_roots:\n  - ./plugins\n",
			},
		},
		{
			id:    "dirtest/TestLoadMissingIncludeFile",
			files: map[string]string{"config.yaml": "include:\n  - missing.yaml\n" + dirtestBase},
		},
		{
			id: "dirtest/TestLoadAbsoluteIncludePath",
			files: map[string]string{
				"config.yaml":  "include:\n  - __CASE_DIR__/plugins.yaml\n" + dirtestBase,
				"plugins.yaml": "plugins:\n  test:\n    enabled: true\n    schedules:\n      - every: 5m\n",
			},
		},
		{
			id: "dirtest/TestLoadCircularInclude",
			files: map[string]string{
				"config.yaml": "include:\n  - a.yaml\nplugin_roots:\n  - ./plugins\n",
				"a.yaml":      "include:\n  - b.yaml\nservice:\n  tick_interval: 60s\n",
				"b.yaml":      "include:\n  - a.yaml\nstate:\n  path: ./test.db\n",
			},
		},
		{
			id: "dirtest/TestLoadCircularIncludeThroughRoot",
			files: map[string]string{
				"config.yaml": "include:\n  - child.yaml\nplugin_roots:\n  - ./plugins\napi:\n  auth:\n    tokens:\n      - token: root-secret\n        scopes: [\"admin\"]\n",
				"child.yaml":  "include:\n  - config.yaml\nstate:\n  path: ./test.db\n",
			},
		},
		{
			id: "dirtest/TestDeepMerge",
			files: map[string]string{
				"config.yaml":   "include:\n  - base.yaml\n  - override.yaml\nstate:\n  path: ./test.db\nplugin_roots:\n  - ./plugins\nplugins:\n  echo:\n    enabled: true\n    schedules:\n      - every: 5m\n",
				"base.yaml":     "service:\n  name: ductile\n  tick_interval: 60s\n  log_level: info\n",
				"override.yaml": "service:\n  log_level: debug\n",
			},
		},
		{
			id:    "dirtest/TestLoadAcceptsIncludedStandaloneWebhooksFileNestedShape",
			vault: map[string]string{"github_webhook_secret": "test-secret"},
			lock:  []string{"webhooks.yaml"},
			files: map[string]string{
				"config.yaml": "include:\n  - webhooks.yaml\n" + dirtestBase + dirtestSecretsBlock +
					"plugins:\n  echo:\n    enabled: true\nwebhooks:\n  listen: \"127.0.0.1:8091\"\n",
				"webhooks.yaml":       "webhooks:\n  endpoints:\n    - path: /webhook/github\n      plugin: echo\n      secret_ref: github_webhook_secret\n      signature_header: X-Hub-Signature-256\n",
				"scopes/webhook.json": "[\"*\"]\n",
			},
		},
		{
			id:    "dirtest/flat-webhooks-shape-in-include",
			vault: map[string]string{"github_webhook_secret": "test-secret"},
			lock:  []string{"webhooks.yaml"},
			files: map[string]string{
				"config.yaml": "include:\n  - webhooks.yaml\n" + dirtestBase + dirtestSecretsBlock +
					"plugins:\n  echo:\n    enabled: true\n",
				"webhooks.yaml": "webhooks:\n  - path: /webhook/github\n    plugin: echo\n    secret_ref: github_webhook_secret\n    signature_header: X-Hub-Signature-256\n",
			},
		},
		{
			id:    "dirtest/TestLoadAcceptsRelayConfigIncludes",
			vault: map[string]string{"relay-lab-v1": "test-secret"},
			files: map[string]string{
				"config.yaml": "include:\n  - relay-instances.yaml\n  - relay-ingress.yaml\n" + dirtestBase + dirtestSecretsBlock +
					"plugins:\n  echo:\n    enabled: true\n",
				"relay-instances.yaml": `instances:
  - name: lab
    enabled: true
    base_url: https://lab.example
    ingress_path: /ingest/peer/home-primary
    secret_ref: relay-lab-v1
    key_id: v1
    timeout: 10s
    allow:
      - backup.ready
`,
				"relay-ingress.yaml": `remote_ingress:
  listen_path: /ingest/peer
  max_body_size: 1MB
  allowed_clock_skew: 5m
  require_key_id: true
  peers:
    - name: home-primary
      enabled: true
      secret_ref: relay-lab-v1
      key_id: v1
      accept:
        - backup.ready
      baggage:
        allow:
          - trace_id
`,
			},
		},
		{
			id:    "dirtest/TestLoadRejectsDuplicateRelayInstanceNames",
			vault: map[string]string{"relay-lab-v1": "test-secret"},
			files: map[string]string{
				"config.yaml": "include:\n  - relay-instances.yaml\n" + dirtestBase + dirtestSecretsBlock +
					"plugins:\n  echo:\n    enabled: true\n",
				"relay-instances.yaml": `instances:
  - name: lab
    enabled: true
    base_url: https://lab-a.example
    ingress_path: /ingest/peer/home-primary
    secret_ref: relay-lab-v1
  - name: lab
    enabled: true
    base_url: https://lab-b.example
    ingress_path: /ingest/peer/home-primary
    secret_ref: relay-lab-v1
`,
			},
		},
	}
}
