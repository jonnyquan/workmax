package workagent

type ApiGroup struct {
	AIChatApiNew
}

// NewAIChatApi creates a new ApiGroup instance
func NewAIChatApi() *ApiGroup {
	return &ApiGroup{
		AIChatApiNew: *NewAIChatApiNew(),
	}
}
