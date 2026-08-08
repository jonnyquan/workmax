package config

type Stripe struct {
	Mode           string                      `mapstructure:"mode" json:"mode" yaml:"mode"`
	Domain         string                      `mapstructure:"domain" json:"domain" yaml:"domain"`
	TestDomain     string                      `mapstructure:"test_domain" json:"test_domain" yaml:"test_domain"`
	CallbackPath   string                      `mapstructure:"callback_path" json:"callback_path" yaml:"callback_path"`
	ReturnPath     string                      `mapstructure:"return_path" json:"return_path" yaml:"return_path"`
	PublicKey      string                      `mapstructure:"public_key" json:"public_key" yaml:"public_key"`
	PrivateKey     string                      `mapstructure:"private_key" json:"private_key" yaml:"private_key"`
	EndpointSecret string                      `mapstructure:"endpoint_secret" json:"endpoint_secret" yaml:"endpoint_secret"`
	Plans          map[string]SubscriptionPlan `mapstructure:"plans" json:"plans" yaml:"plans"`
	CreditPacks    map[string]CreditPack       `mapstructure:"credit_packs" json:"credit_packs" yaml:"credit_packs"`
}

type SubscriptionPlan struct {
	Name           string `mapstructure:"name" json:"name" yaml:"name"`
	PriceID        string `mapstructure:"price_id" json:"price_id" yaml:"price_id"`
	Mode           string `mapstructure:"mode" json:"mode" yaml:"mode"`
	MonthlyPrice   int    `mapstructure:"monthly_price" json:"monthly_price" yaml:"monthly_price"`
	Credits        int    `mapstructure:"credits" json:"credits" yaml:"credits"`
	MonthlyCredits int    `mapstructure:"monthly_credits" json:"monthly_credits" yaml:"monthly_credits"`
	Description    string `mapstructure:"description" json:"description" yaml:"description"`
}

type CreditPack struct {
	Name        string `mapstructure:"name" json:"name" yaml:"name"`
	PriceID     string `mapstructure:"price_id" json:"price_id" yaml:"price_id"`
	Price       int    `mapstructure:"price" json:"price" yaml:"price"`
	Credits     int    `mapstructure:"credits" json:"credits" yaml:"credits"`
	Description string `mapstructure:"description" json:"description" yaml:"description"`
}
