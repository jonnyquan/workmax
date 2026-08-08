package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"server/config"
	"server/service/agentturn"
)

const (
	defaultWorkerMySQLMaxOpen    = 16
	defaultWorkerMySQLMaxIdle    = 4
	maxWorkerMySQLMaxOpen        = 32
	maxWorkerMySQLMaxIdle        = 16
	defaultWorkerMySQLIOTimeout  = 10 * time.Second
	maxWorkerMySQLIOTimeout      = 30 * time.Second
	workerMySQLConnMaxLifetime   = 5 * time.Minute
	workerMySQLConnMaxIdleTime   = time.Minute
	minimumWorkerMySQLTLSVersion = tls.VersionTLS12
)

var (
	errWorkerMySQLSettings       = errors.New("agent-worker MySQL connection settings are unsafe")
	errWorkerMySQLConnection     = errors.New("agent-worker MySQL connection preflight failed")
	errWorkerMySQLNetwork        = errors.New("agent-worker MySQL network preflight failed")
	errWorkerMySQLTLS            = errors.New("agent-worker MySQL TLS preflight failed")
	errWorkerMySQLAuthentication = errors.New("agent-worker MySQL authentication preflight failed")
	errWorkerMySQLDatabase       = errors.New("agent-worker MySQL database selection preflight failed")
	errWorkerMySQLSchema         = errors.New("agent-worker MySQL schema preflight failed")
	errWorkerMySQLSchemaSession  = errors.New("agent-worker MySQL schema session preflight failed")
	errWorkerMySQLSchemaMetadata = errors.New("agent-worker MySQL schema metadata preflight failed")
	errWorkerMySQLSchemaTables   = errors.New("agent-worker MySQL table preflight failed")
	errWorkerMySQLSchemaIndexes  = errors.New("agent-worker MySQL index preflight failed")
	errWorkerMySQLSchemaFKs      = errors.New("agent-worker MySQL foreign-key preflight failed")
	errWorkerMySQLClose          = errors.New("agent-worker MySQL connection close failed")
)

type workerMySQLSettings struct {
	driver       *drivermysql.Config
	databaseName string
	requireTLS   bool
	maxOpen      int
	maxIdle      int
}

// workerValidatedMySQLSettings is the bounded value handoff produced after the
// role-owned parser has built and verified its canonical driver policy. It
// shares no mutable driver/TLS/map state. A future production factory
// reconstructs a fresh driver Config from these scalar fields.
type workerValidatedMySQLSettings struct {
	username      string
	password      string
	address       string
	databaseName  string
	requireTLS    bool
	tlsServerName string
	timeout       time.Duration
	readTimeout   time.Duration
	writeTimeout  time.Duration
	maxOpen       int
	maxIdle       int
}

func freezeWorkerMySQLSettings(input workerMySQLSettings) (workerValidatedMySQLSettings, bool) {
	if input.driver == nil {
		return workerValidatedMySQLSettings{}, false
	}
	tlsServerName := ""
	if input.driver.TLS != nil {
		tlsServerName = input.driver.TLS.ServerName
	}
	frozen := workerValidatedMySQLSettings{
		username:      input.driver.User,
		password:      input.driver.Passwd,
		address:       input.driver.Addr,
		databaseName:  input.databaseName,
		requireTLS:    input.requireTLS,
		tlsServerName: tlsServerName,
		timeout:       input.driver.Timeout,
		readTimeout:   input.driver.ReadTimeout,
		writeTimeout:  input.driver.WriteTimeout,
		maxOpen:       input.maxOpen,
		maxIdle:       input.maxIdle,
	}
	if !frozen.intact() {
		return workerValidatedMySQLSettings{}, false
	}
	// Compare the complete driver value, including fields not carried forward.
	// This makes a future parser change fail closed if it enables file access,
	// plaintext fallback, multi-statements, callbacks or another driver option.
	if !reflect.DeepEqual(input.driver, frozen.freshDriverConfigUnchecked()) {
		return workerValidatedMySQLSettings{}, false
	}
	return frozen, true
}

func (settings workerValidatedMySQLSettings) intact() bool {
	if !validFrozenWorkerMySQLText(settings.username, true) ||
		!validFrozenWorkerMySQLText(settings.password, false) ||
		!validFrozenWorkerMySQLText(settings.databaseName, true) ||
		!validFrozenWorkerMySQLText(settings.address, true) ||
		len(settings.tlsServerName) > int(maxWorkerConfigBytes) ||
		strings.ContainsRune(settings.tlsServerName, 0) ||
		settings.timeout <= 0 || settings.timeout > maxWorkerMySQLIOTimeout ||
		settings.readTimeout <= 0 || settings.readTimeout > maxWorkerMySQLIOTimeout ||
		settings.writeTimeout <= 0 || settings.writeTimeout > maxWorkerMySQLIOTimeout ||
		settings.maxOpen <= 0 || settings.maxOpen > maxWorkerMySQLMaxOpen ||
		settings.maxIdle < 0 || settings.maxIdle > settings.maxOpen ||
		settings.maxIdle > maxWorkerMySQLMaxIdle {
		return false
	}
	host, port, err := net.SplitHostPort(settings.address)
	if err != nil || host == "" || strings.ContainsAny(host, "\x00/\\") {
		return false
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 || strconv.Itoa(portNumber) != port {
		return false
	}
	if settings.requireTLS {
		return settings.tlsServerName == host
	}
	return settings.tlsServerName == "" && !workerMySQLHostIsRemote(host)
}

func validFrozenWorkerMySQLText(value string, requireTrimmed bool) bool {
	if strings.TrimSpace(value) == "" || len(value) > int(maxWorkerConfigBytes) ||
		strings.ContainsRune(value, 0) {
		return false
	}
	return !requireTrimmed || value == strings.TrimSpace(value)
}

func (settings workerValidatedMySQLSettings) freshDriverConfig() (*drivermysql.Config, bool) {
	if !settings.intact() {
		return nil, false
	}
	return settings.freshDriverConfigUnchecked(), true
}

func (settings workerValidatedMySQLSettings) freshDriverConfigUnchecked() *drivermysql.Config {
	driverConfig := drivermysql.NewConfig()
	driverConfig.User = settings.username
	driverConfig.Passwd = settings.password
	driverConfig.Net = "tcp"
	driverConfig.Addr = settings.address
	driverConfig.DBName = settings.databaseName
	driverConfig.ParseTime = true
	driverConfig.Loc = time.UTC
	driverConfig.Logger = &drivermysql.NopLogger{}
	driverConfig.Params = workerMySQLSessionParams()
	driverConfig.Timeout = settings.timeout
	driverConfig.ReadTimeout = settings.readTimeout
	driverConfig.WriteTimeout = settings.writeTimeout
	driverConfig.RejectReadOnly = true
	if settings.requireTLS {
		driverConfig.TLS = &tls.Config{
			MinVersion: minimumWorkerMySQLTLSVersion,
			ServerName: settings.tlsServerName,
		}
	}
	return driverConfig
}

// newWorkerMySQLSettings converts the shared YAML shape into a role-owned
// driver configuration. Remote endpoints get verified TLS even when the
// legacy API Server query string omitted `tls`; insecure/fallback modes and
// driver features that broaden file/auth/query behavior are rejected.
func newWorkerMySQLSettings(mysqlConfig config.GormMysql) (workerMySQLSettings, error) {
	return newWorkerMySQLSettingsWithPolicy(mysqlConfig, false)
}

func newWorkerMySQLSettingsWithPolicy(mysqlConfig config.GormMysql, allowRemotePlaintext bool) (workerMySQLSettings, error) {
	if err := validateWorkerMySQL(mysqlConfig); err != nil {
		return workerMySQLSettings{}, errWorkerMySQLSettings
	}
	host := strings.TrimSpace(mysqlConfig.Path)
	if host != mysqlConfig.Path || strings.ContainsAny(host, "\x00/\\") {
		return workerMySQLSettings{}, errWorkerMySQLSettings
	}
	portNumber, err := strconv.Atoi(strings.TrimSpace(mysqlConfig.Port))
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return workerMySQLSettings{}, errWorkerMySQLSettings
	}
	port := strconv.Itoa(portNumber)
	hostForAddress := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if hostForAddress == "" {
		return workerMySQLSettings{}, errWorkerMySQLSettings
	}

	advanced, err := parseWorkerMySQLAdvanced(mysqlConfig.Config)
	if err != nil {
		return workerMySQLSettings{}, errWorkerMySQLSettings
	}
	driverConfig := drivermysql.NewConfig()
	driverConfig.User = mysqlConfig.Username
	driverConfig.Passwd = mysqlConfig.Password
	driverConfig.Net = "tcp"
	driverConfig.Addr = net.JoinHostPort(hostForAddress, port)
	driverConfig.DBName = mysqlConfig.Dbname
	driverConfig.ParseTime = true
	driverConfig.Loc = time.UTC
	driverConfig.Logger = &drivermysql.NopLogger{}
	// Constructing a fresh driver config after parsing the small role-owned
	// allowlist is deliberate. It prevents recognized DSN options (including
	// unexported options such as timeTruncate) from silently changing memory,
	// authentication or lease-time semantics.
	driverConfig.Params = workerMySQLSessionParams()
	driverConfig.Timeout = boundedWorkerMySQLTimeout(advanced.timeout)
	driverConfig.ReadTimeout = boundedWorkerMySQLTimeout(advanced.readTimeout)
	driverConfig.WriteTimeout = boundedWorkerMySQLTimeout(advanced.writeTimeout)
	driverConfig.RejectReadOnly = true

	remote := workerMySQLHostIsRemote(hostForAddress)
	if remote && advanced.tlsDisabled && !allowRemotePlaintext {
		return workerMySQLSettings{}, errWorkerMySQLSettings
	}
	requireTLS := advanced.tlsRequested || (remote && !allowRemotePlaintext)
	if requireTLS {
		driverConfig.TLS = &tls.Config{
			MinVersion: minimumWorkerMySQLTLSVersion,
			ServerName: hostForAddress,
		}
	}

	maxOpen := boundedWorkerMySQLPool(mysqlConfig.MaxOpenConns, defaultWorkerMySQLMaxOpen, maxWorkerMySQLMaxOpen)
	maxIdle := boundedWorkerMySQLPool(mysqlConfig.MaxIdleConns, defaultWorkerMySQLMaxIdle, maxWorkerMySQLMaxIdle)
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	return workerMySQLSettings{
		driver:       driverConfig,
		databaseName: mysqlConfig.Dbname,
		requireTLS:   requireTLS,
		maxOpen:      maxOpen,
		maxIdle:      maxIdle,
	}, nil
}

func workerMySQLSessionParams() map[string]string {
	return map[string]string{
		"charset":                 "utf8mb4",
		"foreign_key_checks":      "1",
		"unique_checks":           "1",
		"check_constraint_checks": "1",
		"time_zone":               "'+00:00'",
		"transaction_isolation":   "'READ-COMMITTED'",
	}
}

type workerMySQLAdvanced struct {
	tlsRequested bool
	tlsDisabled  bool
	timeout      time.Duration
	readTimeout  time.Duration
	writeTimeout time.Duration
}

// parseWorkerMySQLAdvanced accepts only settings owned by this process. The
// shared config shape historically exposed the complete driver query string;
// treating unknown-but-recognized driver keys as harmless would let config
// alter lease timestamps, packet memory or authentication behavior.
func parseWorkerMySQLAdvanced(raw string) (workerMySQLAdvanced, error) {
	var parsed workerMySQLAdvanced
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "?")
	if raw == "" {
		return parsed, nil
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return workerMySQLAdvanced{}, errWorkerMySQLSettings
	}
	seen := make(map[string]struct{}, len(values))
	for key, candidates := range values {
		canonical := strings.ToLower(strings.TrimSpace(key))
		if canonical == "" || key != strings.TrimSpace(key) || len(candidates) != 1 {
			return workerMySQLAdvanced{}, errWorkerMySQLSettings
		}
		if _, duplicate := seen[canonical]; duplicate {
			return workerMySQLAdvanced{}, errWorkerMySQLSettings
		}
		seen[canonical] = struct{}{}
		value := candidates[0]
		if value == "" || value != strings.TrimSpace(value) {
			return workerMySQLAdvanced{}, errWorkerMySQLSettings
		}
		switch canonical {
		case "charset":
			if !strings.EqualFold(value, "utf8mb4") {
				return workerMySQLAdvanced{}, errWorkerMySQLSettings
			}
		case "foreign_key_checks":
			if value != "1" {
				return workerMySQLAdvanced{}, errWorkerMySQLSettings
			}
		case "parsetime":
			enabled, parseErr := strconv.ParseBool(value)
			if parseErr != nil || !enabled {
				return workerMySQLAdvanced{}, errWorkerMySQLSettings
			}
		case "loc":
			// The value remains accepted for compatibility with the shared
			// config, but every Worker connection is normalized to UTC.
			if !strings.EqualFold(value, "local") && !strings.EqualFold(value, "utc") {
				return workerMySQLAdvanced{}, errWorkerMySQLSettings
			}
		case "tls":
			enabled, parseErr := strconv.ParseBool(value)
			if parseErr != nil {
				return workerMySQLAdvanced{}, errWorkerMySQLSettings
			}
			parsed.tlsRequested = enabled
			parsed.tlsDisabled = !enabled
		case "timeout":
			parsed.timeout, err = parseWorkerMySQLDuration(value)
		case "readtimeout":
			parsed.readTimeout, err = parseWorkerMySQLDuration(value)
		case "writetimeout":
			parsed.writeTimeout, err = parseWorkerMySQLDuration(value)
		default:
			return workerMySQLAdvanced{}, errWorkerMySQLSettings
		}
		if err != nil {
			return workerMySQLAdvanced{}, errWorkerMySQLSettings
		}
	}
	return parsed, nil
}

func parseWorkerMySQLDuration(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, errWorkerMySQLSettings
	}
	return duration, nil
}

func boundedWorkerMySQLPool(configured, fallback, maximum int) int {
	if configured <= 0 {
		return fallback
	}
	if configured > maximum {
		return maximum
	}
	return configured
}

func boundedWorkerMySQLTimeout(configured time.Duration) time.Duration {
	if configured <= 0 {
		return defaultWorkerMySQLIOTimeout
	}
	if configured > maxWorkerMySQLIOTimeout {
		return maxWorkerMySQLIOTimeout
	}
	return configured
}

func workerMySQLHostIsRemote(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

type workerMySQLHandle struct {
	database *gorm.DB
	pool     *sql.DB
	probe    *workerMySQLRuntimeProbe

	closeOnce sync.Once
	closeErr  error
}

func openWorkerMySQL(mysqlConfig config.GormMysql) (*workerMySQLHandle, error) {
	return openWorkerMySQLWithPolicy(mysqlConfig, false)
}

func openWorkerMySQLWithPolicy(mysqlConfig config.GormMysql, allowRemotePlaintext bool) (*workerMySQLHandle, error) {
	settings, err := newWorkerMySQLSettingsWithPolicy(mysqlConfig, allowRemotePlaintext)
	if err != nil {
		return nil, errWorkerMySQLSettings
	}
	connector, err := drivermysql.NewConnector(settings.driver)
	if err != nil {
		return nil, errWorkerMySQLSettings
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(settings.maxOpen)
	pool.SetMaxIdleConns(settings.maxIdle)
	pool.SetConnMaxLifetime(workerMySQLConnMaxLifetime)
	pool.SetConnMaxIdleTime(workerMySQLConnMaxIdleTime)

	database, err := gorm.Open(gormmysql.New(gormmysql.Config{
		Conn:                      pool,
		SkipInitializeWithVersion: true,
		DefaultStringSize:         191,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Discard,
	})
	if err != nil {
		_ = pool.Close()
		return nil, errWorkerMySQLConnection
	}
	return &workerMySQLHandle{
		database: database,
		pool:     pool,
		probe: &workerMySQLRuntimeProbe{
			pool: pool, databaseName: settings.databaseName, requireTLS: settings.requireTLS,
		},
	}, nil
}

func (handle *workerMySQLHandle) Close() error {
	if handle == nil || handle.pool == nil {
		return nil
	}
	handle.closeOnce.Do(func() {
		if err := handle.pool.Close(); err != nil {
			handle.closeErr = errWorkerMySQLClose
		}
	})
	return handle.closeErr
}

// resourceCloser adapts database/sql's context-free Close method to the
// composition ownership protocol. The resource stack supplies the hard
// isolation boundary if a driver fails to return promptly.
func (handle *workerMySQLHandle) resourceCloser() WorkerResourceCloser {
	return WorkerResourceCloseFunc(func(context.Context) error {
		return handle.Close()
	})
}

type workerMySQLRuntimeProbe struct {
	pool         *sql.DB
	databaseName string
	requireTLS   bool
}

func (probe *workerMySQLRuntimeProbe) Startup(ctx context.Context) (resultErr error) {
	connection, err := probe.checkedConnection(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := connection.Close(); resultErr == nil && err != nil {
			resultErr = errWorkerMySQLConnection
		}
	}()
	if err := agentturn.ValidateMySQLRuntimeSchema(ctx, connection); err != nil {
		return classifyWorkerMySQLSchemaError(err)
	}
	return nil
}

func classifyWorkerMySQLSchemaError(err error) error {
	for _, candidate := range []struct {
		cause  error
		stable error
	}{
		{agentturn.ErrMySQLRuntimeSchemaSession, errWorkerMySQLSchemaSession},
		{agentturn.ErrMySQLRuntimeSchemaMetadata, errWorkerMySQLSchemaMetadata},
		{agentturn.ErrMySQLRuntimeSchemaTables, errWorkerMySQLSchemaTables},
		{agentturn.ErrMySQLRuntimeSchemaIndexes, errWorkerMySQLSchemaIndexes},
		{agentturn.ErrMySQLRuntimeSchemaForeignKeys, errWorkerMySQLSchemaFKs},
	} {
		if errors.Is(err, candidate.cause) {
			return candidate.stable
		}
	}
	return errWorkerMySQLSchema
}

func (probe *workerMySQLRuntimeProbe) Check(ctx context.Context) error {
	connection, err := probe.checkedConnection(ctx)
	if err != nil {
		return err
	}
	if err := connection.Close(); err != nil {
		return errWorkerMySQLConnection
	}
	return nil
}

func (probe *workerMySQLRuntimeProbe) checkedConnection(ctx context.Context) (*sql.Conn, error) {
	if probe == nil || probe.pool == nil || ctx == nil {
		return nil, errWorkerMySQLConnection
	}
	connection, err := probe.pool.Conn(ctx)
	if err != nil {
		return nil, classifyWorkerMySQLConnectionError(err)
	}
	closeOnError := func(cause error) (*sql.Conn, error) {
		_ = connection.Close()
		return nil, classifyWorkerMySQLConnectionError(cause)
	}
	if err := connection.PingContext(ctx); err != nil {
		return closeOnError(err)
	}
	var databaseName sql.NullString
	if err := connection.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&databaseName); err != nil {
		return closeOnError(err)
	}
	if !databaseName.Valid || databaseName.String != probe.databaseName {
		return closeOnError(errWorkerMySQLDatabase)
	}
	var foreignKeyChecks int
	if err := connection.QueryRowContext(ctx, `SELECT @@SESSION.foreign_key_checks`).Scan(&foreignKeyChecks); err != nil {
		return closeOnError(err)
	}
	if foreignKeyChecks != 1 {
		_ = connection.Close()
		return nil, errWorkerMySQLSchemaSession
	}
	if probe.requireTLS {
		var variable, cipher string
		if err := connection.QueryRowContext(ctx, `SHOW SESSION STATUS LIKE 'Ssl_cipher'`).Scan(&variable, &cipher); err != nil ||
			!strings.EqualFold(variable, "Ssl_cipher") || strings.TrimSpace(cipher) == "" {
			return closeOnError(errWorkerMySQLTLS)
		}
	}
	return connection, nil
}

// classifyWorkerMySQLConnectionError preserves only an operational category.
// Driver messages can contain the selected host, account or server detail, so
// neither the original error nor its text crosses the command boundary.
func classifyWorkerMySQLConnectionError(err error) error {
	if err == nil {
		return errWorkerMySQLConnection
	}
	for _, stable := range []error{
		errWorkerMySQLNetwork,
		errWorkerMySQLTLS,
		errWorkerMySQLAuthentication,
		errWorkerMySQLDatabase,
	} {
		if errors.Is(err, stable) {
			return stable
		}
	}
	var serverErr *drivermysql.MySQLError
	if errors.As(err, &serverErr) {
		switch serverErr.Number {
		case 1045, 1698, 3118:
			return errWorkerMySQLAuthentication
		case 1044, 1049:
			return errWorkerMySQLDatabase
		case 3159:
			return errWorkerMySQLTLS
		default:
			return errWorkerMySQLConnection
		}
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"tls", "x509", "certificate"} {
		if strings.Contains(message, marker) {
			return errWorkerMySQLTLS
		}
	}
	var dnsErr *net.DNSError
	var operationErr *net.OpError
	if errors.As(err, &dnsErr) || errors.As(err, &operationErr) ||
		errors.Is(err, context.DeadlineExceeded) {
		return errWorkerMySQLNetwork
	}
	if strings.Contains(message, "access denied") || strings.Contains(message, "authentication") {
		return errWorkerMySQLAuthentication
	}
	if strings.Contains(message, "unknown database") {
		return errWorkerMySQLDatabase
	}
	return errWorkerMySQLConnection
}

// checkProductionWorkerDatabase is the explicit no-traffic preflight used by
// `agent-worker -check-database`. It opens no listener, runs no migrations,
// starts no loops and always closes the pool before returning.
func checkProductionWorkerDatabase(ctx context.Context, snapshot workerStartupSnapshot) (resultErr error) {
	handle, err := openWorkerMySQLWithPolicy(snapshot.MySQL(), snapshot.DatabasePlaintextAllowed())
	if err != nil {
		return err
	}
	defer func() {
		if err := handle.Close(); resultErr == nil && err != nil {
			resultErr = err
		}
	}()
	return handle.probe.Startup(ctx)
}
