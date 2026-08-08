package account

type ServiceGroup struct {
	AccountService
	JwtService
	ApiKeyService
	CheckinService
	PermissionService
}
