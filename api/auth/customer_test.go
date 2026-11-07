package auth

import (
	"testing"
	"time"

	"api-server/config"
)

func TestCustomerJWTSessionVersion(t *testing.T) {
	originalKey, originalExpiration := config.JWTKey, config.JWTExpiration
	t.Cleanup(func() {
		config.JWTKey = originalKey
		config.JWTExpiration = originalExpiration
	})
	config.JWTKey = "test-customer-session-version-signing-key"
	config.JWTExpiration = time.Hour

	token, err := CustomerJWTIssue(42, "13800138000", 7)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := CustomerJWTDecrypt(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.CustomerID != 42 || claims.Phone != "13800138000" || claims.SessionVersion != 7 {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}
