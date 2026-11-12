//go:build !unix

package nativewake

import (
	"context"
	"errors"
)

type Binding struct {
	Socket string `json:"socket"`
	Token  string `json:"token,omitempty"`
}

func ValidateBinding(Binding) error {
	return errors.New("native Unix inbox integration is unavailable on this platform; use standard MCP")
}
func Post(context.Context, Binding, string) (string, error) {
	return "unavailable", ValidateBinding(Binding{})
}
