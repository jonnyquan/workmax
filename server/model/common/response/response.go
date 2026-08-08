package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type Response struct {
	Code    int         `json:"code"`
	Data    interface{} `json:"data"`
	Message string      `json:"message"`
}

const (
	ERROR   = 0
	SUCCESS = 1
)

func Result(code int, data interface{}, message string, c *gin.Context) {
	// 开始时间
	c.JSON(http.StatusOK, Response{
		code,
		data,
		message,
	})
}

func Ok(c *gin.Context) {
	Result(SUCCESS, map[string]interface{}{}, "Operation success", c)
}

func OkWithMessage(message string, c *gin.Context) {
	Result(SUCCESS, map[string]interface{}{}, message, c)
}

func OkWithLogin(data interface{}, c *gin.Context) {
	Result(SUCCESS, data, "Login success", c)
}

func OkWithData(data interface{}, c *gin.Context) {
	Result(SUCCESS, data, "Query success", c)
}

func OkWithDetailed(data interface{}, message string, c *gin.Context) {
	Result(SUCCESS, data, message, c)
}

func OkWithDetailedWithProtobuf(data interface{}, message string, c *gin.Context) {
	c.ProtoBuf(http.StatusOK, data)
}

func Fail(c *gin.Context) {
	Result(ERROR, map[string]interface{}{}, "Operation failed", c)
}

func FailWithMessage(message string, c *gin.Context) {
	Result(ERROR, map[string]interface{}{}, message, c)
}

func FailWithDetailed(data interface{}, message string, c *gin.Context) {
	Result(ERROR, data, message, c)
}

func NotFound(c *gin.Context) {
	Result(http.StatusNotFound, map[string]interface{}{}, "Request path not found", c)
}

func OkWithBool(data bool, msg string, c *gin.Context) {
	Result(SUCCESS, data, msg, c)
}
