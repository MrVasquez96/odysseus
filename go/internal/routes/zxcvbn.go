package routes

import (
	zxcvbn "github.com/nbutton23/zxcvbn-go"
)

func init() {
	// Replace the fallback password scorer with the real zxcvbn implementation.
	scorePasswordImpl = func(password string) int {
		return zxcvbn.PasswordStrength(password, nil).Score
	}
}
