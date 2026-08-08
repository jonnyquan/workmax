package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/viper"

	"server/config"
)

const (
	workerConfigEnvironment = "BODO_CONFIG"
	workerLocalConfigPath   = "config.yaml"
)

var (
	errWorkerConfigArguments = errors.New("agent-worker configuration arguments are invalid")
	errWorkerConfigPath      = errors.New("agent-worker configuration path is empty")
	errWorkerConfigRead      = errors.New("agent-worker configuration could not be read")
	errWorkerConfigDecode    = errors.New("agent-worker configuration is malformed")
	errWorkerRoleConfig      = errors.New("agent-worker rollout configuration is invalid")
	errWorkerMySQLRequired   = errors.New("agent-worker MySQL configuration is incomplete")
	errWorkerMySQLPool       = errors.New("agent-worker MySQL connection limits must be non-negative")
	errWorkerConfigLoader    = errors.New("agent-worker configuration loader is unavailable")
)

type workerConfigSource string

const (
	workerConfigSourceCommandLine workerConfigSource = "command-line"
	workerConfigSourceEnvironment workerConfigSource = "environment"
	workerConfigSourceLocal       workerConfigSource = "local-default"
)

type workerConfigSelection struct {
	path                   string
	source                 workerConfigSource
	checkDatabase          bool
	allowDatabasePlaintext bool
}

// workerStartupSnapshot is the immutable, role-scoped view retained by the
// worker process. It deliberately contains values rather than pointers to a
// Viper instance or the shared Server config, so neither a file change nor a
// mutable global can alter the decision made at startup.
type workerStartupSnapshot struct {
	source  workerConfigSource
	digest  [sha256.Size]byte
	mysql   config.GormMysql
	rollout config.AgentPlatformRollout
	// checkDatabase is a process command, not rollout state. It permits an
	// explicit, read-only database/schema preflight while Worker remains off.
	checkDatabase          bool
	allowDatabasePlaintext bool
}

func (snapshot workerStartupSnapshot) Source() workerConfigSource {
	return snapshot.source
}

func (snapshot workerStartupSnapshot) Digest() [sha256.Size]byte {
	return snapshot.digest
}

func (snapshot workerStartupSnapshot) MySQL() config.GormMysql {
	return snapshot.mysql
}

func (snapshot workerStartupSnapshot) Rollout() config.AgentPlatformRollout {
	return snapshot.rollout
}

func (snapshot workerStartupSnapshot) WorkerEnabled() bool {
	return snapshot.rollout.Durable.Worker == config.DurableWorkerOn
}

func (snapshot workerStartupSnapshot) DatabaseCheckRequested() bool {
	return snapshot.checkDatabase
}

func (snapshot workerStartupSnapshot) DatabasePlaintextAllowed() bool {
	return snapshot.checkDatabase && snapshot.allowDatabasePlaintext
}

type workerRolloutConfigDocument struct {
	Rollout *workerRolloutDocument `mapstructure:"agent_platform_rollout"`
}

type workerMySQLConfigDocument struct {
	MySQL config.GormMysql `mapstructure:"mysql_system"`
}

// workerRolloutDocument is a decode-time role projection, not merely a
// validation-time filter. Viper never attempts to coerce API, Desktop or
// credential fields into Worker-owned types, so malformed configuration owned
// by another process cannot block this role's clean Worker-off exit.
type workerRolloutDocument struct {
	Durable struct {
		Worker config.DurableWorkerMode `mapstructure:"worker"`
	} `mapstructure:"durable_turn"`
	Readiness struct {
		SQLStore              bool `mapstructure:"sql_store"`
		WorkerLeaseFencing    bool `mapstructure:"worker_lease_fencing"`
		TransactionalOutbox   bool `mapstructure:"transactional_outbox"`
		ExactlyOnceSettlement bool `mapstructure:"exactly_once_settlement"`
	} `mapstructure:"readiness"`
}

func (document *workerRolloutDocument) configProjection() *config.AgentPlatformRollout {
	if document == nil {
		return nil
	}
	return &config.AgentPlatformRollout{
		Durable: config.DurableTurnRollout{Worker: document.Durable.Worker},
		Readiness: config.AgentPlatformReadiness{
			SQLStore:              document.Readiness.SQLStore,
			WorkerLeaseFencing:    document.Readiness.WorkerLeaseFencing,
			TransactionalOutbox:   document.Readiness.TransactionalOutbox,
			ExactlyOnceSettlement: document.Readiness.ExactlyOnceSettlement,
		},
	}
}

// loadWorkerStartupSnapshot selects and reads exactly one configuration file.
// Callers inject getenv and readFile so precedence and the zero-dependency
// Worker-off path are testable without consulting the developer's real
// environment or configuration.
func loadWorkerStartupSnapshot(
	args []string,
	getenv func(string) string,
	readFile func(string) ([]byte, error),
) (workerStartupSnapshot, error) {
	var zero workerStartupSnapshot
	if getenv == nil || readFile == nil {
		return zero, errWorkerConfigLoader
	}

	selection, err := selectWorkerConfig(args, getenv)
	if err != nil {
		return zero, err
	}

	readBytes, err := readFile(selection.path)
	if err != nil {
		// Paths and underlying filesystem errors are intentionally omitted: the
		// selected path can itself disclose deployment topology.
		return zero, fmt.Errorf("%w: source=%s", errWorkerConfigRead, selection.source)
	}
	// Take ownership of the buffer used for parsing. Test readers and future
	// embedded readers may reuse their returned slice; neither clearing our
	// parsing buffer nor a later caller mutation may affect the other side.
	raw := append([]byte(nil), readBytes...)
	defer clear(raw)

	digest := sha256.Sum256(raw)
	reader := viper.New()
	reader.SetConfigType("yaml")
	if err := reader.ReadConfig(bytes.NewReader(raw)); err != nil {
		return zero, fmt.Errorf("%w: source=%s", errWorkerConfigDecode, selection.source)
	}

	var rolloutDocument workerRolloutConfigDocument
	if err := reader.Unmarshal(&rolloutDocument); err != nil {
		return zero, fmt.Errorf("%w: source=%s", errWorkerConfigDecode, selection.source)
	}
	rolloutProjection := rolloutDocument.Rollout.configProjection()
	if err := rolloutProjection.ValidateWorkerRole(); err != nil {
		// ValidateWorkerRole errors are safe today, but this boundary keeps the
		// public startup error stable if config validation gains secret-bearing
		// context later.
		return zero, fmt.Errorf("%w: source=%s", errWorkerRoleConfig, selection.source)
	}

	rollout := config.EffectiveAgentPlatformRollout(rolloutProjection)
	mysql := config.GormMysql{}
	if rollout.Durable.Worker == config.DurableWorkerOn || selection.checkDatabase {
		// Decode database fields only after the Worker role is known to be on.
		// Worker-off therefore cannot be coupled even to structurally invalid
		// database settings it will never use.
		var mysqlDocument workerMySQLConfigDocument
		if err := reader.Unmarshal(&mysqlDocument); err != nil {
			return zero, fmt.Errorf("%w: source=%s", errWorkerConfigDecode, selection.source)
		}
		if err := validateWorkerMySQL(mysqlDocument.MySQL); err != nil {
			return zero, err
		}
		mysql = mysqlDocument.MySQL
	}

	return workerStartupSnapshot{
		source:                 selection.source,
		digest:                 digest,
		mysql:                  mysql,
		rollout:                rollout,
		checkDatabase:          selection.checkDatabase,
		allowDatabasePlaintext: selection.allowDatabasePlaintext,
	}, nil
}

func selectWorkerConfig(args []string, getenv func(string) string) (workerConfigSelection, error) {
	if getenv == nil {
		return workerConfigSelection{}, errWorkerConfigLoader
	}

	flags := flag.NewFlagSet("agent-worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var commandLinePath string
	var checkDatabase bool
	var allowDatabasePlaintext bool
	flags.StringVar(&commandLinePath, "c", "", "choose configuration file")
	flags.BoolVar(&checkDatabase, "check-database", false, "run a read-only database/schema preflight and exit")
	flags.BoolVar(&allowDatabasePlaintext, "allow-remote-plaintext-database", false,
		"allow remote plaintext only for the explicit read-only database preflight")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return workerConfigSelection{}, errWorkerConfigArguments
	}
	if allowDatabasePlaintext && !checkDatabase {
		return workerConfigSelection{}, errWorkerConfigArguments
	}

	commandLineSelected := false
	flags.Visit(func(selected *flag.Flag) {
		if selected.Name == "c" {
			commandLineSelected = true
		}
	})
	if commandLineSelected {
		if strings.TrimSpace(commandLinePath) == "" {
			return workerConfigSelection{}, fmt.Errorf("%w: source=%s", errWorkerConfigPath, workerConfigSourceCommandLine)
		}
		return workerConfigSelection{
			path: commandLinePath, source: workerConfigSourceCommandLine,
			checkDatabase: checkDatabase, allowDatabasePlaintext: allowDatabasePlaintext,
		}, nil
	}

	if environmentPath := getenv(workerConfigEnvironment); environmentPath != "" {
		if strings.TrimSpace(environmentPath) == "" {
			return workerConfigSelection{}, fmt.Errorf("%w: source=%s", errWorkerConfigPath, workerConfigSourceEnvironment)
		}
		return workerConfigSelection{
			path: environmentPath, source: workerConfigSourceEnvironment,
			checkDatabase: checkDatabase, allowDatabasePlaintext: allowDatabasePlaintext,
		}, nil
	}

	return workerConfigSelection{
		path: workerLocalConfigPath, source: workerConfigSourceLocal,
		checkDatabase: checkDatabase, allowDatabasePlaintext: allowDatabasePlaintext,
	}, nil
}

func validateWorkerMySQL(mysql config.GormMysql) error {
	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "path", value: mysql.Path},
		{name: "port", value: mysql.Port},
		{name: "db-name", value: mysql.Dbname},
		{name: "username", value: mysql.Username},
		{name: "password", value: mysql.Password},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("%w: field=mysql_system.%s", errWorkerMySQLRequired, required.name)
		}
	}
	if mysql.MaxIdleConns < 0 {
		return fmt.Errorf("%w: field=mysql_system.max-idle-conns", errWorkerMySQLPool)
	}
	if mysql.MaxOpenConns < 0 {
		return fmt.Errorf("%w: field=mysql_system.max-open-conns", errWorkerMySQLPool)
	}
	return nil
}
