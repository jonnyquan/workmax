package globals

import (
	"server/config"
	"strings"
)

type ChannelConfig interface {
	GetType() string
	GetName() string
	GetModelReflect(model string) string
	GetRetry() int
	GetRandomSecret() string
	SplitRandomSecret(num int) []string
	GetEndpoint() string
	ProcessError(err error) error
	GetId() int
	GetProxy() config.ProxyConfig
}

type BaseChannelConfig struct {
	Type         string
	Name         string
	ModelReflect map[string]string
	Retry        int
	RandomSecret string
	Endpoint     string
	Id           int
	Proxy        config.ProxyConfig
}

func (b *BaseChannelConfig) GetType() string {
	return b.Type
}

func (b *BaseChannelConfig) GetName() string {
	return b.Name
}

func (b *BaseChannelConfig) GetModelReflect(model string) string {
	if reflect, ok := b.ModelReflect[model]; ok {
		return reflect
	}
	return model
}

func (b *BaseChannelConfig) GetRetry() int {
	return b.Retry
}

func (b *BaseChannelConfig) GetRandomSecret() string {
	return b.RandomSecret
}

func (b *BaseChannelConfig) SplitRandomSecret(num int) []string {
	secrets := strings.Split(b.RandomSecret, ",")
	if len(secrets) < num {
		return secrets
	}
	return secrets[:num]
}

func (b *BaseChannelConfig) GetEndpoint() string {
	return b.Endpoint
}

func (b *BaseChannelConfig) ProcessError(err error) error {
	return err
}

func (b *BaseChannelConfig) GetId() int {
	return b.Id
}

func (b *BaseChannelConfig) GetProxy() config.ProxyConfig {
	return b.Proxy
}
