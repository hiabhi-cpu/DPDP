package handlers

import (
	"context"

	"github.com/hiabhi-cpu/shared/hospitaljwt"
)

// StubProvider returns a TokenProvider that always yields tok (test helper).
func StubProvider(tok string) hospitaljwt.TokenProvider { return stubProvider(tok) }

type stubProvider string

func (s stubProvider) Token(_ context.Context) (string, error) { return string(s), nil }
