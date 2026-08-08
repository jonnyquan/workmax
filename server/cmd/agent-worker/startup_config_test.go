package main

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"server/config"
)

const workerOffConfig = `agent_platform_rollout:
  durable_turn:
    worker: off
`

const workerOnConfig = `mysql_system:
  path: db.internal
  port: "3306"
  db-name: workmax_contract
  username: worker
  password: test-only-password
  max-idle-conns: 2
  max-open-conns: 4
agent_platform_rollout:
  durable_turn:
    worker: on
  readiness:
    sql_store: true
    worker_lease_fencing: true
    transactional_outbox: true
    exactly_once_settlement: true
`

func TestWorkerStartupConfigPathPrecedence(t *testing.T) {
	for _, test := range []struct {
		name            string
		args            []string
		environmentPath string
		wantPath        string
		wantSource      workerConfigSource
		wantEnvReads    int
	}{
		{
			name: "command line wins without consulting environment", args: []string{"-c", "/selected/by-cli.yaml"},
			environmentPath: "/selected/by-env.yaml", wantPath: "/selected/by-cli.yaml",
			wantSource: workerConfigSourceCommandLine, wantEnvReads: 0,
		},
		{
			name: "environment wins over local default", environmentPath: "/selected/by-env.yaml",
			wantPath: "/selected/by-env.yaml", wantSource: workerConfigSourceEnvironment, wantEnvReads: 1,
		},
		{
			name: "local default is last", wantPath: "config.yaml",
			wantSource: workerConfigSourceLocal, wantEnvReads: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var envReads, fileReads int
			var readPath string
			snapshot, err := loadWorkerStartupSnapshot(
				test.args,
				func(name string) string {
					envReads++
					if name != workerConfigEnvironment {
						t.Fatalf("environment lookup = %q", name)
					}
					return test.environmentPath
				},
				func(path string) ([]byte, error) {
					fileReads++
					readPath = path
					return []byte(workerOffConfig), nil
				},
			)
			if err != nil {
				t.Fatalf("loadWorkerStartupSnapshot(): %v", err)
			}
			if readPath != test.wantPath || snapshot.Source() != test.wantSource {
				t.Fatalf("selected path/source = %q/%q, want %q/%q",
					readPath, snapshot.Source(), test.wantPath, test.wantSource)
			}
			if fileReads != 1 || envReads != test.wantEnvReads {
				t.Fatalf("reads: file=%d env=%d, want file=1 env=%d", fileReads, envReads, test.wantEnvReads)
			}
		})
	}
}

func TestWorkerStartupDatabaseCheckUsesSelectedWorkerOffConfig(t *testing.T) {
	input := strings.Replace(workerOnConfig, "    worker: on\n", "    worker: off\n", 1)
	snapshot, err := loadWorkerStartupSnapshot(
		[]string{"-check-database", "-c", "/selected/check.yaml"},
		func(string) string { return "/ignored/environment.yaml" },
		func(path string) ([]byte, error) {
			if path != "/selected/check.yaml" {
				t.Fatalf("path = %q, want selected command-line path", path)
			}
			return []byte(input), nil
		},
	)
	if err != nil {
		t.Fatalf("loadWorkerStartupSnapshot(): %v", err)
	}
	if !snapshot.DatabaseCheckRequested() || snapshot.WorkerEnabled() || snapshot.MySQL().Dbname != "workmax_contract" {
		t.Fatalf("snapshot = requested:%t worker:%t db:%q",
			snapshot.DatabaseCheckRequested(), snapshot.WorkerEnabled(), snapshot.MySQL().Dbname)
	}
	if snapshot.Source() != workerConfigSourceCommandLine {
		t.Fatalf("source = %q, want command-line", snapshot.Source())
	}
}

func TestWorkerStartupPlaintextOverrideIsScopedToExplicitDatabaseCheck(t *testing.T) {
	var reads int
	if _, err := loadWorkerStartupSnapshot([]string{"-allow-remote-plaintext-database"},
		func(string) string { return "" },
		func(string) ([]byte, error) {
			reads++
			return []byte(workerOffConfig), nil
		},
	); !errors.Is(err, errWorkerConfigArguments) {
		t.Fatalf("plaintext-only error = %v, want invalid arguments", err)
	}
	if reads != 0 {
		t.Fatalf("plaintext-only invocation read %d files, want zero", reads)
	}

	input := strings.Replace(workerOnConfig, "    worker: on\n", "    worker: off\n", 1)
	snapshot, err := loadWorkerStartupSnapshot(
		[]string{"-check-database", "-allow-remote-plaintext-database"},
		func(string) string { return "" },
		func(string) ([]byte, error) { return []byte(input), nil },
	)
	if err != nil {
		t.Fatalf("explicit plaintext database check: %v", err)
	}
	if !snapshot.DatabaseCheckRequested() || !snapshot.DatabasePlaintextAllowed() || snapshot.WorkerEnabled() {
		t.Fatalf("snapshot scope = check:%t plaintext:%t worker:%t",
			snapshot.DatabaseCheckRequested(), snapshot.DatabasePlaintextAllowed(), snapshot.WorkerEnabled())
	}
}

func TestWorkerStartupDatabaseCheckFailsClosedOnInvalidMySQL(t *testing.T) {
	input := `mysql_system:
  path: db.internal
  port: "3306"
  db-name: workmax_contract
  username: worker
  password: ""
agent_platform_rollout:
  durable_turn:
    worker: off
`
	_, err := loadWorkerStartupSnapshot([]string{"-check-database"},
		func(string) string { return "" },
		func(string) ([]byte, error) { return []byte(input), nil },
	)
	if !errors.Is(err, errWorkerMySQLRequired) {
		t.Fatalf("error = %v, want required MySQL field", err)
	}
}

func TestWorkerStartupConfigRejectsExplicitEmptyCommandLinePath(t *testing.T) {
	for _, args := range [][]string{{"-c="}, {"-c", ""}, {"-c", " \t "}} {
		var fileReads int
		_, err := loadWorkerStartupSnapshot(args,
			func(string) string { return "/must-not-be-used.yaml" },
			func(string) ([]byte, error) {
				fileReads++
				return []byte(workerOffConfig), nil
			},
		)
		if !errors.Is(err, errWorkerConfigPath) {
			t.Fatalf("args %q: error = %v, want errWorkerConfigPath", args, err)
		}
		if fileReads != 0 {
			t.Fatalf("args %q read %d files, want zero", args, fileReads)
		}
	}
}

func TestWorkerStartupConfigRejectsWhitespaceEnvironmentPath(t *testing.T) {
	var fileReads int
	_, err := loadWorkerStartupSnapshot(nil,
		func(string) string { return " \t " },
		func(string) ([]byte, error) {
			fileReads++
			return []byte(workerOffConfig), nil
		},
	)
	if !errors.Is(err, errWorkerConfigPath) || !strings.Contains(err.Error(), "source=environment") {
		t.Fatalf("error = %v, want redacted environment-path error", err)
	}
	if fileReads != 0 {
		t.Fatalf("read %d files, want zero", fileReads)
	}
}

func TestWorkerStartupConfigRejectsInvalidArgumentsBeforeReading(t *testing.T) {
	for _, args := range [][]string{{"-unknown"}, {"positional.yaml"}, {"-c"}} {
		var fileReads int
		_, err := loadWorkerStartupSnapshot(args,
			func(string) string { return "" },
			func(string) ([]byte, error) {
				fileReads++
				return []byte(workerOffConfig), nil
			},
		)
		if !errors.Is(err, errWorkerConfigArguments) {
			t.Fatalf("args %q: error = %v, want errWorkerConfigArguments", args, err)
		}
		if fileReads != 0 {
			t.Fatalf("args %q read %d files, want zero", args, fileReads)
		}
	}
}

func TestWorkerStartupConfigReadAndDecodeErrorsAreRedacted(t *testing.T) {
	const secretPath = "/deployment/SECRET_PATH/config.yaml"
	const secretCause = "SECRET_CAUSE_password=hunter2"
	_, err := loadWorkerStartupSnapshot([]string{"-c", secretPath},
		func(string) string { return "" },
		func(string) ([]byte, error) { return nil, errors.New(secretCause) },
	)
	if !errors.Is(err, errWorkerConfigRead) {
		t.Fatalf("read error = %v, want errWorkerConfigRead", err)
	}
	if !strings.Contains(err.Error(), "source=command-line") {
		t.Fatalf("read error = %v, want safe source kind", err)
	}
	assertErrorOmits(t, err, secretPath, secretCause, "hunter2")

	const secretValue = "SECRET_YAML_PASSWORD"
	malformed := "mysql_system:\n  password: " + secretValue + "\nagent_platform_rollout: ["
	_, err = loadWorkerStartupSnapshot([]string{"-c", secretPath},
		func(string) string { return "" },
		func(string) ([]byte, error) { return []byte(malformed), nil },
	)
	if !errors.Is(err, errWorkerConfigDecode) {
		t.Fatalf("decode error = %v, want errWorkerConfigDecode", err)
	}
	if !strings.Contains(err.Error(), "source=command-line") {
		t.Fatalf("decode error = %v, want safe source kind", err)
	}
	assertErrorOmits(t, err, secretPath, secretValue)
}

func TestWorkerStartupConfigReadsExactlyOnceAndHashesSelectedBytes(t *testing.T) {
	raw := []byte(workerOffConfig)
	wantDigest := sha256.Sum256(raw)
	var reads int
	snapshot, err := loadWorkerStartupSnapshot(nil,
		func(string) string { return "" },
		func(path string) ([]byte, error) {
			reads++
			if path != workerLocalConfigPath {
				t.Fatalf("path = %q, want %q", path, workerLocalConfigPath)
			}
			return append([]byte(nil), raw...), nil
		},
	)
	if err != nil {
		t.Fatalf("loadWorkerStartupSnapshot(): %v", err)
	}
	if reads != 1 {
		t.Fatalf("read count = %d, want 1", reads)
	}
	if snapshot.Digest() != wantDigest {
		t.Fatalf("digest = %x, want %x", snapshot.Digest(), wantDigest)
	}
}

func TestWorkerStartupSnapshotIsUnaffectedBySourceOrReturnedValueMutation(t *testing.T) {
	source := []byte(workerOnConfig)
	snapshot, err := loadWorkerStartupSnapshot(nil,
		func(string) string { return "" },
		func(string) ([]byte, error) { return source, nil },
	)
	if err != nil {
		t.Fatalf("loadWorkerStartupSnapshot(): %v", err)
	}

	copy(source, []byte(workerOffConfig))
	rollout := snapshot.Rollout()
	rollout.Durable.Worker = config.DurableWorkerOff
	mysql := snapshot.MySQL()
	mysql.Password = "mutated"
	digest := snapshot.Digest()
	digest[0] ^= 0xff

	if !snapshot.WorkerEnabled() || snapshot.Rollout().Durable.Worker != config.DurableWorkerOn {
		t.Fatal("mutating source or returned rollout changed the startup decision")
	}
	if snapshot.MySQL().Password != "test-only-password" {
		t.Fatal("mutating returned MySQL config changed the snapshot")
	}
	if snapshot.Digest() == digest {
		t.Fatal("mutating the returned digest changed the snapshot digest")
	}
}

func TestWorkerOffIgnoresDatabaseAndOtherProcessRoles(t *testing.T) {
	input := `mysql_system:
  path: [structurally-invalid-for-database]
  max-idle-conns: {structurally: invalid-for-database}
  max-open-conns: [structurally-invalid-for-database]
agent_platform_rollout:
  credential:
    - structurally-invalid-for-credential-role
  durable_turn:
    worker: off
    public_api: [structurally-invalid-for-api-role]
    canary_percent: {invalid: for-api-role}
  desktop:
    agent_transport: [structurally-invalid-for-desktop-role]
  readiness:
    atomic_live_event_stream: {invalid: for-api-role}
    token_rollover_complete: [invalid-for-credential-role]
`
	snapshot, err := loadWorkerStartupSnapshot(nil,
		func(string) string { return "" },
		func(string) ([]byte, error) { return []byte(input), nil },
	)
	if err != nil {
		t.Fatalf("Worker-off role was blocked by unowned config: %v", err)
	}
	if snapshot.WorkerEnabled() {
		t.Fatal("Worker-off config produced an enabled snapshot")
	}
	if snapshot.MySQL() != (config.GormMysql{}) {
		t.Fatalf("Worker-off snapshot retained ignored database config: %+v", snapshot.MySQL())
	}
	rollout := snapshot.Rollout()
	if rollout.Durable.PublicAPI != config.DurablePublicAPIOff ||
		rollout.Desktop.AgentTransport != config.DesktopAgentTransportLegacy ||
		rollout.Credential.AgentResource != config.CredentialRolloutOff ||
		rollout.Readiness.AtomicLiveEventStream {
		t.Fatalf("snapshot retained another role's configuration: %+v", rollout)
	}
}

func TestWorkerOnDecodesOnlyWorkerOwnedRolloutProjection(t *testing.T) {
	input := strings.Replace(workerOnConfig,
		"agent_platform_rollout:\n  durable_turn:\n    worker: on\n",
		`agent_platform_rollout:
  credential: [structurally-invalid-for-credential-role]
  durable_turn:
    worker: on
    public_api: [structurally-invalid-for-api-role]
  desktop: [structurally-invalid-for-desktop-role]
`, 1)
	input = strings.Replace(input,
		"  readiness:\n    sql_store: true\n",
		"  readiness:\n    atomic_live_event_stream: [structurally-invalid-for-api-role]\n    sql_store: true\n", 1)

	snapshot, err := loadWorkerStartupSnapshot(nil,
		func(string) string { return "" },
		func(string) ([]byte, error) { return []byte(input), nil },
	)
	if err != nil {
		t.Fatalf("Worker-on role was blocked by unowned config: %v", err)
	}
	if !snapshot.WorkerEnabled() {
		t.Fatal("Worker-on projection was not enabled")
	}
	rollout := snapshot.Rollout()
	if rollout.Durable.PublicAPI != config.DurablePublicAPIOff ||
		rollout.Desktop.AgentTransport != config.DesktopAgentTransportLegacy ||
		rollout.Credential.DesktopResource != config.CredentialRolloutOff ||
		rollout.Readiness.AtomicLiveEventStream {
		t.Fatalf("snapshot retained another role's configuration: %+v", rollout)
	}
}

func TestMissingRolloutDefaultsWorkerOff(t *testing.T) {
	snapshot, err := loadWorkerStartupSnapshot(nil,
		func(string) string { return "" },
		func(string) ([]byte, error) { return []byte("system:\n  env: test\n"), nil },
	)
	if err != nil {
		t.Fatalf("missing rollout was not defaulted closed: %v", err)
	}
	if snapshot.WorkerEnabled() || snapshot.Rollout().Durable.Worker != config.DurableWorkerOff {
		t.Fatalf("missing rollout = %+v, want Worker off", snapshot.Rollout().Durable)
	}
}

func TestWorkerStartupConfigRejectsInvalidWorkerMode(t *testing.T) {
	input := `agent_platform_rollout:
  durable_turn:
    worker: definitely-not-a-worker-mode
`
	_, err := loadWorkerStartupSnapshot(nil,
		func(string) string { return "" },
		func(string) ([]byte, error) { return []byte(input), nil },
	)
	if !errors.Is(err, errWorkerRoleConfig) {
		t.Fatalf("invalid Worker mode error = %v, want errWorkerRoleConfig", err)
	}
}

func TestWorkerOnValidatesRoleBeforeDatabase(t *testing.T) {
	input := strings.Replace(workerOnConfig, "    exactly_once_settlement: true\n", "", 1)
	input = strings.Replace(input, "  password: test-only-password\n", "  password: SECRET_ROLE_PASSWORD\n", 1)
	_, err := loadWorkerStartupSnapshot(nil,
		func(string) string { return "" },
		func(string) ([]byte, error) { return []byte(input), nil },
	)
	if !errors.Is(err, errWorkerRoleConfig) {
		t.Fatalf("error = %v, want errWorkerRoleConfig", err)
	}
	assertErrorOmits(t, err, "SECRET_ROLE_PASSWORD")
}

func TestWorkerOnValidatesRequiredMySQLFieldsAndConnectionLimits(t *testing.T) {
	for _, test := range []struct {
		name        string
		old         string
		replacement string
		want        error
		field       string
	}{
		{name: "host", old: "  path: db.internal\n", replacement: "  path: \"\"\n", want: errWorkerMySQLRequired, field: "mysql_system.path"},
		{name: "port", old: "  port: \"3306\"\n", replacement: "  port: \" \"\n", want: errWorkerMySQLRequired, field: "mysql_system.port"},
		{name: "database", old: "  db-name: workmax_contract\n", replacement: "  db-name: \"\"\n", want: errWorkerMySQLRequired, field: "mysql_system.db-name"},
		{name: "username", old: "  username: worker\n", replacement: "  username: \"\"\n", want: errWorkerMySQLRequired, field: "mysql_system.username"},
		{name: "password", old: "  password: test-only-password\n", replacement: "  password: \"\"\n", want: errWorkerMySQLRequired, field: "mysql_system.password"},
		{name: "negative idle pool", old: "  max-idle-conns: 2\n", replacement: "  max-idle-conns: -1\n", want: errWorkerMySQLPool, field: "mysql_system.max-idle-conns"},
		{name: "negative open pool", old: "  max-open-conns: 4\n", replacement: "  max-open-conns: -1\n", want: errWorkerMySQLPool, field: "mysql_system.max-open-conns"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := strings.Replace(workerOnConfig, test.old, test.replacement, 1)
			var reads int
			_, err := loadWorkerStartupSnapshot(nil,
				func(string) string { return "" },
				func(string) ([]byte, error) {
					reads++
					return []byte(input), nil
				},
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if !strings.Contains(err.Error(), "field="+test.field) {
				t.Fatalf("error = %v, want safe field %q", err, test.field)
			}
			if reads != 1 {
				t.Fatalf("read count = %d, want 1", reads)
			}
			assertErrorOmits(t, err, "db.internal", "workmax_contract", "test-only-password")
		})
	}
}

func TestWorkerOnRejectsStructurallyInvalidMySQLConfig(t *testing.T) {
	input := strings.Replace(workerOnConfig, "  max-open-conns: 4\n", "  max-open-conns: [4]\n", 1)
	_, err := loadWorkerStartupSnapshot(nil,
		func(string) string { return "" },
		func(string) ([]byte, error) { return []byte(input), nil },
	)
	if !errors.Is(err, errWorkerConfigDecode) {
		t.Fatalf("error = %v, want errWorkerConfigDecode", err)
	}
	assertErrorOmits(t, err, "db.internal", "test-only-password")
}

func TestWorkerOnAcceptsValidRoleAndDatabaseSnapshot(t *testing.T) {
	snapshot, err := loadWorkerStartupSnapshot([]string{"-c=/safe/test/path.yaml"},
		func(string) string { return "/ignored/environment.yaml" },
		func(string) ([]byte, error) { return []byte(workerOnConfig), nil },
	)
	if err != nil {
		t.Fatalf("loadWorkerStartupSnapshot(): %v", err)
	}
	if !snapshot.WorkerEnabled() || snapshot.MySQL().Dbname != "workmax_contract" {
		t.Fatalf("snapshot = worker=%t mysql-db=%q", snapshot.WorkerEnabled(), snapshot.MySQL().Dbname)
	}
}

func TestWorkerStartupConfigDoesNotExpandFieldEnvironmentPlaceholders(t *testing.T) {
	const placeholder = "${WORKMAX_WORKER_DB_PASSWORD}"
	t.Setenv("WORKMAX_WORKER_DB_PASSWORD", "expanded-secret")
	input := strings.Replace(workerOnConfig, "test-only-password", placeholder, 1)
	snapshot, err := loadWorkerStartupSnapshot([]string{"-c=/safe/test/path.yaml"},
		func(string) string { return "" },
		func(string) ([]byte, error) { return []byte(input), nil },
	)
	if err != nil {
		t.Fatalf("loadWorkerStartupSnapshot(): %v", err)
	}
	if got := snapshot.MySQL().Password; got != placeholder {
		t.Fatalf("password = %q, want literal placeholder", got)
	}
}

func TestWorkerStartupConfigRejectsUnavailableLoader(t *testing.T) {
	if _, err := loadWorkerStartupSnapshot(nil, nil, func(string) ([]byte, error) { return nil, nil }); !errors.Is(err, errWorkerConfigLoader) {
		t.Fatalf("nil getenv error = %v, want errWorkerConfigLoader", err)
	}
	if _, err := loadWorkerStartupSnapshot(nil, func(string) string { return "" }, nil); !errors.Is(err, errWorkerConfigLoader) {
		t.Fatalf("nil readFile error = %v, want errWorkerConfigLoader", err)
	}
}

func assertErrorOmits(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	message := err.Error()
	for _, value := range forbidden {
		if value != "" && strings.Contains(message, value) {
			t.Fatalf("error %q leaked %q", message, value)
		}
	}
}
