package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"api-server/config"
	authutil "api-server/util/authentication"
)

type CustomerClaims struct {
	CustomerID uint   `json:"customer_id"`
	Phone      string `json:"phone"`
	jwt.RegisteredClaims
}

func CustomerJWTIssue(customerID uint, phone string) (string, error) {
	now := time.Now()
	claims := CustomerClaims{
		CustomerID: customerID,
		Phone:      phone,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "customer",
			ExpiresAt: jwt.NewNumericDate(now.Add(config.JWTExpiration)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	authutil.PrepareRegisteredClaims(&claims.RegisteredClaims)
	return authutil.SignHS256(&claims)
}

func CustomerJWTDecrypt(tokenString string) (*CustomerClaims, error) {
	claims := &CustomerClaims{}
	token, err := authutil.ParseHS256(tokenString, claims)
	if err != nil || !token.Valid || claims.Subject != "customer" {
		return nil, fmt.Errorf("invalid customer token")
	}
	return claims, nil
}
