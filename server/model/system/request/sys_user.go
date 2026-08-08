package request

type Login struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type SignUp struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	InviteCode string `json:"inviteCode"`
}

type ChangePassword struct {
	Token    string `json:"token"`
	Password string `json:"newPassword"`
}

type ForgotPassword struct {
	Email string `json:"email"`
}
