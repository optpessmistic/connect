//go:build !darwin

package sessionstore

import (
	"errors"
	"github.com/zalando/go-keyring"
)

type System struct {
	// Tests use a unique namespace, never the application's saved login.
	serviceName, accountName string
}

const service = "com.surplustoken.connect.session"
const account = "https://surplustoken.com"

func (s System) names() (string, string) {
	if s.serviceName != "" && s.accountName != "" {
		return s.serviceName, s.accountName
	}
	return service, account
}
func (s System) Load() (string, error) {
	serviceName, accountName := s.names()
	token, err := keyring.Get(serviceName, accountName)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("无法读取系统凭据库")
	}
	return token, nil
}
func (s System) Save(token string) error {
	serviceName, accountName := s.names()
	if keyring.Set(serviceName, accountName, token) != nil {
		return errors.New("无法保存登录状态到系统凭据库")
	}
	return nil
}
func (s System) Delete() error {
	serviceName, accountName := s.names()
	err := keyring.Delete(serviceName, accountName)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return errors.New("无法清除系统凭据库中的登录状态")
	}
	return nil
}
