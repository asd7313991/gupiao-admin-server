package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"

	"api-server/api/auth"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

func CustomerTokenVerify(c *gin.Context) {
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if token == "" {
		response.ReturnError(c, response.UNAUTHENTICATED, "请先登录")
		return
	}
	claims, err := auth.CustomerJWTDecrypt(token)
	if err != nil {
		response.ReturnError(c, response.UNAUTHENTICATED, "登录已过期，请重新登录")
		return
	}
	var customer system.Customer
	if err := pgdb.GetClient().Select("id").First(&customer, claims.CustomerID).Error; err != nil {
		response.ReturnError(c, response.UNAUTHENTICATED, "账户不存在或已被删除，请重新登录")
		return
	}
	c.Set("customer_id", claims.CustomerID)
	c.Set("customer_phone", claims.Phone)
	c.Next()
}

func GetCurrentCustomerID(c *gin.Context) uint {
	value, exists := c.Get("customer_id")
	if !exists {
		return 0
	}
	id, _ := value.(uint)
	return id
}
