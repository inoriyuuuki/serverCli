package service

import "errors"

// Sentinel errors mapped to HTTP statuses by the API layer.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrLocked             = errors.New("account temporarily locked")
	ErrNotAuthenticated   = errors.New("not authenticated")
	ErrForbidden          = errors.New("forbidden")
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("conflict")
	ErrBadRequest         = errors.New("bad request")
	ErrOffline            = errors.New("node offline")
	ErrAmbiguous          = errors.New("ambiguous selector, use node id or ip:port")
	ErrTerminal           = errors.New("operation not allowed in terminal state")
	ErrUnavailable        = errors.New("unavailable")
	ErrDisabled           = errors.New("disabled")
)
