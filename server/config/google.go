package config

type Google struct {
	ClientID     string `mapstructure:"client_id" json:"client_id" yaml:"client_id"`
	ClientSecret string `mapstructure:"client_secret" json:"client_secret" yaml:"client_secret"`
	RedirectURL  string `mapstructure:"redirect_url" json:"redirect_url" yaml:"redirect_url"`
}
