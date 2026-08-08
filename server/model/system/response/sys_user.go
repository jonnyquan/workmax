package response

type MembershipResponse struct {
	Member       string `json:"member"`
	MemberStatus string `json:"memberStatus"`
	MemberStart  string `json:"memberStart"`
	MemberEnd    string `json:"memberEnd"`
}

type LoginUserInfo struct {
	Email      string             `json:"email"`
	AuthEmail  int                `json:"authEmail"`
	Avatar     string             `json:"avatar"`
	Nickname   string             `json:"nickname"`
	Id         uint               `json:"id"`
	Authority  []string           `json:"authority"`
	Membership MembershipResponse `json:"membership"`
}

type LoginResponse struct {
	User      LoginUserInfo `json:"user"`
	Token     string        `json:"token"`
	ExpiresAt int64         `json:"expiresAt"`
}
