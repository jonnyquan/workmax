package config

type System struct {
	Addr          string       `mapstructure:"addr" json:"addr" yaml:"addr"`
	RouterPrefix  string       `mapstructure:"router-prefix" json:"router-prefix" yaml:"router-prefix"`
	FrontendURL   string       `mapstructure:"frontend_url" json:"frontend_url" yaml:"frontend_url"`
	BackendURL    string       `mapstructure:"backend_url" json:"backend_url" yaml:"backend_url"`
	Env           string       `mapstructure:"env" json:"env" yaml:"env"`
	AdminEmail    string       `mapstructure:"admin_email" json:"admin_email" yaml:"admin_email"`
	ServerRunPath string       `mapstructure:"server_run_path" json:"server_run_path" yaml:"server_run_path"`
	Cron          Cron         `mapstructure:"cron" json:"cron" yaml:"cron"`
	SMTP          SMTP         `mapstructure:"smtp" json:"smtp" yaml:"smtp"`
	Proxy         *ProxyConfig `mapstructure:"proxy" json:"proxy" yaml:"proxy"`
}

const (
	NoneProxyType = iota
	HttpProxyType
	HttpsProxyType
	Socks5ProxyType
)

type ProxyConfig struct {
	ProxyType int    `json:"proxy_type" mapstructure:"proxytype"`
	Proxy     string `json:"proxy" mapstructure:"proxy"`
	Username  string `json:"username" mapstructure:"username"`
	Password  string `json:"password" mapstructure:"password"`
}

type Cron struct {
	Enable                   bool `mapstructure:"enable" json:"enable" yaml:"enable"`
	EmailTask                bool `mapstructure:"email_task" json:"email_task" yaml:"email_task"`
	BlogAutoGeneration       bool `mapstructure:"blog_auto_generation" json:"blog_auto_generation" yaml:"blog_auto_generation"`
	TagStats                 bool `mapstructure:"tag_stats" json:"tag_stats" yaml:"tag_stats"`
	GenerationObjectCleanup  bool `mapstructure:"generation_object_cleanup" json:"generation_object_cleanup" yaml:"generation_object_cleanup"`
	CreditReservationSweeper bool `mapstructure:"credit_reservation_sweeper" json:"credit_reservation_sweeper" yaml:"credit_reservation_sweeper"`
	CommerceEventReconciler  bool `mapstructure:"commerce_event_reconciler" json:"commerce_event_reconciler" yaml:"commerce_event_reconciler"`
}

type SMTP struct {
	Host     string `mapstructure:"host" json:"host" yaml:"host"`
	Port     int    `mapstructure:"port" json:"port" yaml:"port"`
	Username string `mapstructure:"username" json:"username" yaml:"username"`
	Password string `mapstructure:"password" json:"password" yaml:"password"`
	From     string `mapstructure:"from" json:"from" yaml:"from"`
	SSL      bool   `mapstructure:"ssl" json:"ssl" yaml:"ssl"`
}
