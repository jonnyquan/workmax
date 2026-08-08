module server

go 1.24.1

toolchain go1.24.4

require (
	github.com/aws/aws-sdk-go-v2 v1.41.1
	github.com/aws/aws-sdk-go-v2/config v1.32.7
	github.com/aws/aws-sdk-go-v2/credentials v1.19.7
	github.com/aws/aws-sdk-go-v2/service/s3 v1.96.0
	github.com/fsnotify/fsnotify v1.7.0
	github.com/fvbock/endless v0.0.0-20170109170031-447134032cb6
	github.com/gin-gonic/gin v1.10.1
	github.com/glebarez/sqlite v1.11.0
	github.com/go-pdf/fpdf v0.9.0
	github.com/go-redis/redis/v8 v8.11.5
	github.com/go-sql-driver/mysql v1.8.1
	github.com/goccy/go-json v0.10.5
	github.com/golang-jwt/jwt/v4 v4.5.2
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/jonnyquan/claude-agent-sdk-go v0.0.0-20260725181726-31b018f42ae4
	github.com/ledongthuc/pdf v0.0.0-20250511090121-5959a4027728
	github.com/lestrrat-go/file-rotatelogs v2.4.0+incompatible
	github.com/lionsoul2014/ip2region/binding/golang v0.0.0-20250508043914-ed57fa5c5274
	github.com/natefinch/lumberjack v2.0.0+incompatible
	github.com/pkoukk/tiktoken-go v0.1.7
	github.com/robfig/cron v1.2.0
	github.com/sirupsen/logrus v1.9.3
	github.com/songzhibin97/gkit v1.2.13
	github.com/spf13/viper v1.18.2
	github.com/stretchr/testify v1.11.1
	github.com/stripe/stripe-go/v80 v80.2.1
	github.com/tmc/langchaingo v0.1.13
	github.com/yclw/go-vtracer v0.1.1
	go.uber.org/zap v1.21.0
	golang.org/x/crypto v0.43.0
	golang.org/x/exp v0.0.0-20230905200255-921286631fa9
	golang.org/x/image v0.25.0
	golang.org/x/net v0.46.0
	golang.org/x/oauth2 v0.30.0
	golang.org/x/sync v0.17.0
	golang.org/x/text v0.30.0
	gopkg.in/gomail.v2 v2.0.0-20160411212932-81ebce5c23df
	gopkg.in/mail.v2 v2.3.1
	gopkg.in/yaml.v3 v3.0.1
	gorm.io/driver/mysql v1.5.7
	gorm.io/gorm v1.30.0
)

require (
	cloud.google.com/go/compute/metadata v0.3.0 // indirect
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/BurntSushi/toml v1.5.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.4 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.0.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.35.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.41.6 // indirect
	github.com/aws/smithy-go v1.24.0 // indirect
	github.com/bytedance/sonic v1.13.2 // indirect
	github.com/bytedance/sonic/loader v0.2.4 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/cloudwego/base64x v0.1.5 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.8 // indirect
	github.com/gin-contrib/sse v1.0.0 // indirect
	github.com/glebarez/go-sqlite v1.21.2 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.26.0 // indirect
	github.com/hashicorp/hcl v1.0.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/lestrrat-go/strftime v1.1.0 // indirect
	github.com/magiconair/properties v1.8.7 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/onsi/gomega v1.27.3 // indirect
	github.com/pelletier/go-toml/v2 v2.2.3 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/sagikazarmark/locafero v0.4.0 // indirect
	github.com/sagikazarmark/slog-shim v0.1.0 // indirect
	github.com/sourcegraph/conc v0.3.0 // indirect
	github.com/spf13/afero v1.11.0 // indirect
	github.com/spf13/cast v1.6.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.2.12 // indirect
	go.uber.org/atomic v1.9.0 // indirect
	go.uber.org/multierr v1.9.0 // indirect
	golang.org/x/arch v0.15.0 // indirect
	golang.org/x/sys v0.37.0 // indirect
	google.golang.org/protobuf v1.36.6 // indirect
	gopkg.in/alexcesaro/quotedprintable.v3 v3.0.0-20150716171945-2caba252f4dc // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/ini.v1 v1.67.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	modernc.org/libc v1.22.5 // indirect
	modernc.org/mathutil v1.5.0 // indirect
	modernc.org/memory v1.5.0 // indirect
	modernc.org/sqlite v1.23.1 // indirect
)
