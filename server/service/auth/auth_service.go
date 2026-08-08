package auth

import (
	"errors"
	"server/globals"
	"server/model"
	"server/utils"
)

type AuthService struct{}

// Login 登录
func (s *AuthService) Login(u *model.User) (userInfo *model.User, err error) {
	var user model.User
	err = globals.GraDBs["system"].Where("email = ?", u.Email).First(&user).Error
	if err == nil {
		if ok := utils.BcryptCheck(u.Password, user.Password); !ok {
			return nil, errors.New("密码错误")
		}
	}
	return &user, err
}
