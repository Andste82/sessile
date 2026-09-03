package auth

import "errors"

var (
	// ErrUserExists is returned by Create when the username is already taken
	// (compared case-insensitively).
	ErrUserExists = errors.New("username already exists")
	// ErrInvalidCredentials is returned by Verify on a wrong username or
	// password. Deliberately one error for both: telling them apart lets a
	// caller enumerate valid usernames.
	ErrInvalidCredentials = errors.New("invalid username or password")
	// ErrAlreadyBootstrapped is returned when bootstrap is attempted after a
	// user already exists.
	ErrAlreadyBootstrapped = errors.New("server already has an admin account")
	// ErrLastAdmin guards the admin-management endpoints (§12b M11) against
	// deleting or demoting the only admin, which would leave the server with
	// no way to reach its own admin panel.
	ErrLastAdmin = errors.New("cannot remove or demote the last admin")
	// ErrUserNotFound is returned by operations on an id that doesn't exist.
	ErrUserNotFound = errors.New("user not found")
)
