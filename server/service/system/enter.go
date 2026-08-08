package system

type ServiceGroup struct {
	SystemLogService SystemLogService
}

var (
	SystemLogServiceInstance = NewSystemLogService()
)
