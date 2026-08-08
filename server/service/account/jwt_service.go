package account

type JwtService struct{}

//@author: [wangrui19970405](https://github.com/wangrui19970405)
//@function: GetRedisJWT
//@description: 从redis取jwt
//@param: userName string
//@return: redisJWT string, err error

//func (jwtService *JwtService) GetRedisJWT(userName string) (redisJWT string, err error) {
//	redisJWT, err = global.PalRedis.Get(context.Background(), userName).Result()
//	return redisJWT, err
//}

//@author: [wangrui19970405](https://github.com/wangrui19970405)
//@function: SetRedisJWT
//@description: jwt存入redis并设置过期时间
//@param: jwt string, userName string
//@return: err error

//func (jwtService *JwtService) SetRedisJWT(jwt string, userName string) (err error) {
//	// 此处过期时间等于jwt过期时间
//	dr, err := utils.ParseDuration(global.PalConfig.JWT.ExpiresTime)
//	if err != nil {
//		return err
//	}
//	timer := dr
//	err = global.PalRedis.Set(context.Background(), userName, jwt, timer).Err()
//	return err
//}

// JWT blacklist functionality removed - using stateless JWT tokens
