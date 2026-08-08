package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"server/config"
	"server/service/agentturn"
)

func workerMySQLConfigForTest(host, advanced string) config.GormMysql {
	return config.GormMysql{GeneralDB: config.GeneralDB{
		Path: host, Port: "3306", Dbname: "workmax_contract",
		Username: "worker", Password: "SECRET_WORKER_PASSWORD",
		Config: advanced, MaxIdleConns: 10, MaxOpenConns: 200,
	}}
}

func TestWorkerMySQLSettingsUpgradeRemoteConfigToVerifiedTLSAndBoundResources(t *testing.T) {
	settings, err := newWorkerMySQLSettings(workerMySQLConfigForTest(
		"db.example.invalid", "charset=utf8mb4&parseTime=True&loc=Local",
	))
	if err != nil {
		t.Fatalf("newWorkerMySQLSettings(): %v", err)
	}
	if !settings.requireTLS || settings.driver.TLS == nil || settings.driver.TLS.InsecureSkipVerify {
		t.Fatalf("remote TLS settings = require:%t config:%+v", settings.requireTLS, settings.driver.TLS)
	}
	if settings.driver.TLS.ServerName != "db.example.invalid" ||
		settings.driver.TLS.MinVersion < tls.VersionTLS12 {
		t.Fatalf("TLS identity/min version = %q/%d", settings.driver.TLS.ServerName, settings.driver.TLS.MinVersion)
	}
	if settings.maxOpen != maxWorkerMySQLMaxOpen || settings.maxIdle != 10 {
		t.Fatalf("bounded pool = open:%d idle:%d", settings.maxOpen, settings.maxIdle)
	}
	if settings.driver.Timeout != defaultWorkerMySQLIOTimeout ||
		settings.driver.ReadTimeout != defaultWorkerMySQLIOTimeout ||
		settings.driver.WriteTimeout != defaultWorkerMySQLIOTimeout {
		t.Fatalf("bounded defaults = dial:%s read:%s write:%s",
			settings.driver.Timeout, settings.driver.ReadTimeout, settings.driver.WriteTimeout)
	}
	if !settings.driver.ParseTime || !settings.driver.RejectReadOnly ||
		settings.driver.Params["charset"] != "utf8mb4" || settings.driver.Params["foreign_key_checks"] != "1" ||
		settings.driver.Params["unique_checks"] != "1" ||
		settings.driver.Params["check_constraint_checks"] != "1" ||
		settings.driver.Params["time_zone"] != "'+00:00'" ||
		settings.driver.Params["transaction_isolation"] != "'READ-COMMITTED'" || len(settings.driver.Params) != 6 {
		t.Fatalf("normalized driver contract = parse:%t reject-read-only:%t params:%v",
			settings.driver.ParseTime, settings.driver.RejectReadOnly, settings.driver.Params)
	}
	if settings.driver.Loc != time.UTC || !settings.driver.CheckConnLiveness ||
		settings.driver.ConnectionAttributes != "" || settings.driver.Collation != "" {
		t.Fatalf("driver invariants = loc:%v liveness:%t attrs:%q collation:%q",
			settings.driver.Loc, settings.driver.CheckConnLiveness,
			settings.driver.ConnectionAttributes, settings.driver.Collation)
	}
}

func TestWorkerMySQLSettingsReplaceDummyTLSIdentity(t *testing.T) {
	settings, err := newWorkerMySQLSettings(workerMySQLConfigForTest(
		"db.example.invalid", "charset=utf8mb4&parseTime=true&tls=true",
	))
	if err != nil {
		t.Fatalf("newWorkerMySQLSettings(): %v", err)
	}
	if settings.driver.TLS == nil || settings.driver.TLS.ServerName != "db.example.invalid" {
		t.Fatalf("TLS server name = %+v, want selected host", settings.driver.TLS)
	}
}

func TestWorkerMySQLSettingsAllowLoopbackWithoutWeakeningRemotePolicy(t *testing.T) {
	settings, err := newWorkerMySQLSettings(workerMySQLConfigForTest(
		"127.0.0.1", "charset=utf8mb4&parseTime=true",
	))
	if err != nil {
		t.Fatalf("newWorkerMySQLSettings(): %v", err)
	}
	if settings.requireTLS || settings.driver.TLS != nil {
		t.Fatalf("loopback TLS settings = require:%t config:%+v", settings.requireTLS, settings.driver.TLS)
	}
}

func TestWorkerMySQLSettingsPlaintextOverrideIsExplicitAndDoesNotDowngradeTLS(t *testing.T) {
	settings, err := newWorkerMySQLSettingsWithPolicy(workerMySQLConfigForTest(
		"db.example.invalid", "charset=utf8mb4&parseTime=true",
	), true)
	if err != nil {
		t.Fatalf("explicit plaintext settings: %v", err)
	}
	if settings.requireTLS || settings.driver.TLS != nil {
		t.Fatalf("explicit plaintext unexpectedly retained TLS: require=%t config=%+v",
			settings.requireTLS, settings.driver.TLS)
	}

	settings, err = newWorkerMySQLSettingsWithPolicy(workerMySQLConfigForTest(
		"db.example.invalid", "charset=utf8mb4&parseTime=true&tls=true",
	), true)
	if err != nil {
		t.Fatalf("explicit TLS under plaintext-capable check: %v", err)
	}
	if !settings.requireTLS || settings.driver.TLS == nil {
		t.Fatal("plaintext capability downgraded an explicit TLS request")
	}
}

func TestWorkerMySQLSettingsRejectUnsafeOptionsWithoutLeakingSecrets(t *testing.T) {
	for name, advanced := range map[string]string{
		"plaintext fallback":  "charset=utf8mb4&tls=preferred",
		"insecure TLS":        "charset=utf8mb4&tls=skip-verify",
		"all files":           "charset=utf8mb4&allowAllFiles=true",
		"cleartext auth":      "charset=utf8mb4&allowCleartextPasswords=true",
		"old auth":            "charset=utf8mb4&allowOldPasswords=true",
		"multiple statements": "charset=utf8mb4&multiStatements=true",
		"interpolation":       "charset=utf8mb4&interpolateParams=true",
		"foreign keys off":    "charset=utf8mb4&foreign_key_checks=0",
		"unique checks off":   "charset=utf8mb4&unique_checks=0",
		"checks off":          "charset=utf8mb4&check_constraint_checks=0",
		"wrong time zone":     "charset=utf8mb4&time_zone=%2B08%3A00",
		"unsafe isolation":    "charset=utf8mb4&transaction_isolation=SERIALIZABLE",
		"wrong charset":       "charset=latin1",
		"unknown session var": "charset=utf8mb4&sql_mode=SECRET_SQL_MODE",
		"removed strict mode": "charset=utf8mb4&strict=SECRET_STRICT_VALUE",
		"time truncation":     "charset=utf8mb4&timeTruncate=1h",
		"packet size":         "charset=utf8mb4&maxAllowedPacket=1073741824",
		"connection attrs":    "charset=utf8mb4&connectionAttributes=secret:value",
		"collation":           "charset=utf8mb4&collation=utf8mb4_general_ci",
		"liveness off":        "charset=utf8mb4&checkConnLiveness=false",
		"parse time off":      "charset=utf8mb4&parseTime=false",
		"remote TLS off":      "charset=utf8mb4&tls=false",
		"duplicate option":    "charset=utf8mb4&CHARSET=utf8mb4",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newWorkerMySQLSettings(workerMySQLConfigForTest("db.example.invalid", advanced))
			if !errors.Is(err, errWorkerMySQLSettings) {
				t.Fatalf("error = %v, want stable settings rejection", err)
			}
			for _, secret := range []string{"SECRET_WORKER_PASSWORD", "SECRET_SQL_MODE", "SECRET_STRICT_VALUE", "db.example.invalid"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error %q leaked %q", err, secret)
				}
			}
		})
	}
}

func TestWorkerMySQLSettingsBoundExplicitTimeoutsAndPoolRelationship(t *testing.T) {
	mysqlConfig := workerMySQLConfigForTest("localhost",
		"charset=utf8mb4&timeout=2h&readTimeout=3h&writeTimeout=4h")
	mysqlConfig.MaxOpenConns = 2
	mysqlConfig.MaxIdleConns = 10
	settings, err := newWorkerMySQLSettings(mysqlConfig)
	if err != nil {
		t.Fatalf("newWorkerMySQLSettings(): %v", err)
	}
	if settings.driver.Timeout != maxWorkerMySQLIOTimeout ||
		settings.driver.ReadTimeout != maxWorkerMySQLIOTimeout ||
		settings.driver.WriteTimeout != maxWorkerMySQLIOTimeout {
		t.Fatalf("capped timeouts = dial:%s read:%s write:%s",
			settings.driver.Timeout, settings.driver.ReadTimeout, settings.driver.WriteTimeout)
	}
	if settings.maxOpen != 2 || settings.maxIdle != 2 {
		t.Fatalf("pool = open:%d idle:%d, want 2/2", settings.maxOpen, settings.maxIdle)
	}
}

func TestOpenWorkerMySQLConstructsWithoutImplicitNetworkIOAndCloses(t *testing.T) {
	handle, err := openWorkerMySQL(workerMySQLConfigForTest(
		"db.example.invalid", "charset=utf8mb4&parseTime=true",
	))
	if err != nil {
		t.Fatalf("openWorkerMySQL() performed I/O or failed construction: %v", err)
	}
	if handle.database == nil || handle.pool == nil || handle.probe == nil {
		t.Fatalf("incomplete handle = %+v", handle)
	}
	if got := handle.pool.Stats().MaxOpenConnections; got != maxWorkerMySQLMaxOpen {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, maxWorkerMySQLMaxOpen)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
}

func TestExecuteWorkerDatabaseCheckBoundsAndSanitizesImplementations(t *testing.T) {
	snapshot := workerStartupSnapshot{}
	if err, outcome := executeWorkerDatabaseCheck(context.Background(), time.Second,
		func(context.Context, workerStartupSnapshot) error { return nil }, snapshot,
	); err != nil || outcome != workerDatabaseCheckSucceeded {
		t.Fatalf("success = outcome:%d err:%v", outcome, err)
	}

	secretErr := errors.New("mysql://worker:SECRET@db.internal/workmax")
	if err, outcome := executeWorkerDatabaseCheck(context.Background(), time.Second,
		func(context.Context, workerStartupSnapshot) error { return secretErr }, snapshot,
	); !errors.Is(err, errWorkerDatabaseCheckFailed) || outcome != workerDatabaseCheckFailed ||
		strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unsafe failure = outcome:%d err:%v", outcome, err)
	}

	if err, outcome := executeWorkerDatabaseCheck(context.Background(), time.Second,
		func(context.Context, workerStartupSnapshot) error { panic("SECRET_PANIC") }, snapshot,
	); !errors.Is(err, errWorkerDatabaseCheckFailed) || outcome != workerDatabaseCheckFailed {
		t.Fatalf("panic = outcome:%d err:%v", outcome, err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	started := time.Now()
	err, outcome := executeWorkerDatabaseCheck(context.Background(), 20*time.Millisecond,
		func(context.Context, workerStartupSnapshot) error {
			defer close(finished)
			close(entered)
			<-release
			return nil
		}, snapshot)
	if !errors.Is(err, errWorkerDatabaseCheckTimedOut) || outcome != workerDatabaseCheckTimedOut {
		t.Fatalf("timeout = outcome:%d err:%v", outcome, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("non-cooperative check blocked its hard timeout")
	}
	<-entered
	close(release)
	<-finished
}

func TestClassifyWorkerMySQLConnectionErrorReturnsOnlyStableCategories(t *testing.T) {
	for name, test := range map[string]struct {
		err  error
		want error
	}{
		"authentication": {err: &mysql.MySQLError{Number: 1045, Message: "SECRET_AUTH_DETAIL"}, want: errWorkerMySQLAuthentication},
		"database":       {err: &mysql.MySQLError{Number: 1049, Message: "SECRET_DATABASE_DETAIL"}, want: errWorkerMySQLDatabase},
		"TLS":            {err: errors.New("tls: SECRET_CERTIFICATE_DETAIL"), want: errWorkerMySQLTLS},
		"DNS":            {err: &net.DNSError{Err: "SECRET_DNS_DETAIL", Name: "secret.internal"}, want: errWorkerMySQLNetwork},
		"generic":        {err: errors.New("SECRET_DRIVER_DETAIL"), want: errWorkerMySQLConnection},
	} {
		t.Run(name, func(t *testing.T) {
			got := classifyWorkerMySQLConnectionError(test.err)
			if !errors.Is(got, test.want) {
				t.Fatalf("category = %v, want %v", got, test.want)
			}
			assertErrorOmits(t, got, "SECRET", "secret.internal")
		})
	}
}

func TestClassifyWorkerMySQLSchemaErrorReturnsOnlyStableCategories(t *testing.T) {
	for name, test := range map[string]struct {
		err  error
		want error
	}{
		"session":      {err: agentturn.ErrMySQLRuntimeSchemaSession, want: errWorkerMySQLSchemaSession},
		"metadata":     {err: agentturn.ErrMySQLRuntimeSchemaMetadata, want: errWorkerMySQLSchemaMetadata},
		"tables":       {err: agentturn.ErrMySQLRuntimeSchemaTables, want: errWorkerMySQLSchemaTables},
		"indexes":      {err: agentturn.ErrMySQLRuntimeSchemaIndexes, want: errWorkerMySQLSchemaIndexes},
		"foreign keys": {err: agentturn.ErrMySQLRuntimeSchemaForeignKeys, want: errWorkerMySQLSchemaFKs},
		"generic":      {err: errors.New("SECRET_SCHEMA_DETAIL"), want: errWorkerMySQLSchema},
	} {
		t.Run(name, func(t *testing.T) {
			got := classifyWorkerMySQLSchemaError(test.err)
			if !errors.Is(got, test.want) {
				t.Fatalf("category = %v, want %v", got, test.want)
			}
			assertErrorOmits(t, got, "SECRET")
		})
	}
}
