package hosts

import "errors"

// ErrNotFound is returned by Update/Delete for an id that doesn't exist in
// this store — which, since a Store is always opened for one specific
// user, also covers a client probing another user's host id (§4.5, §6):
// there's no way to tell "wrong id" from "someone else's host" apart, and
// there doesn't need to be.
var ErrNotFound = errors.New("host not found")
