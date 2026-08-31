package sshpty

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// remoteAppendKeyCommand appends a public key (piped on stdin) to the
// connecting account's authorized_keys, idempotently — running it twice
// with the same key is a no-op the second time. The key is never
// interpolated into the command string, which avoids any shell-quoting of
// attacker- or typo-prone key material. Stdin is read into $key exactly
// once — an earlier version re-read stdin for both the grep check and the
// append, and the second read always came back empty because the first
// had already consumed it, silently leaving authorized_keys untouched.
const remoteAppendKeyCommand = `sh -c 'umask 077; mkdir -p ~/.ssh; touch ~/.ssh/authorized_keys; key=$(cat); grep -qxF "$key" ~/.ssh/authorized_keys || printf "%s\n" "$key" >> ~/.ssh/authorized_keys'`

// ExchangeKeys sets up passwordless login on target: it generates a fresh
// ed25519 keypair, dials target using password auth (through the same TOFU
// HostKeyCallback Start uses — an unrecognized or changed host key aborts
// this exactly like it aborts a normal connect), and appends the new public
// key to the remote account's ~/.ssh/authorized_keys.
//
// password is used only for this one connection and is never written
// anywhere by this function — the caller (the API handler) is responsible
// for never persisting it either (PROJECT_PLAN.md §4.5.2).
//
// fingerprint identifies the newly generated key (SHA256, same format as
// the host-key fingerprints elsewhere in this package) — not the host's own
// host key, which is unaffected by this call.
func ExchangeKeys(target Target, username, password string) (privateKeyPEM, publicKeyLine, keyType, fingerprint, keyComment string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("generate keypair: %w", err)
	}

	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("build signer: %w", err)
	}
	publicKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("marshal public key: %w", err)
	}

	address := ensurePort(target.Address)
	cfg := &ssh.ClientConfig{
		User:            username,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: pinnedHostKeyCallback(target.TrustedHostKeyFingerprint),
		Timeout:         dialTimeout,
	}

	client, err := ssh.Dial("tcp", address, cfg)
	if err != nil {
		var unknown *ErrHostKeyUnknown
		if errors.As(err, &unknown) {
			return "", "", "", "", "", unknown
		}
		var changed *ErrHostKeyChanged
		if errors.As(err, &changed) {
			return "", "", "", "", "", changed
		}
		return "", "", "", "", "", fmt.Errorf("ssh dial %s: %w", address, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("ssh new session: %w", err)
	}
	defer session.Close()

	session.Stdin = bytes.NewReader(ssh.MarshalAuthorizedKey(publicKey))
	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err := session.Run(remoteAppendKeyCommand); err != nil {
		return "", "", "", "", "", fmt.Errorf("append authorized_keys on %s: %w (%s)", address, err, stderr.String())
	}

	comment := fmt.Sprintf("sessile-%s@%s", username, target.Address)
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("marshal generated private key: %w", err)
	}

	authorizedLine := string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(publicKey)))
	return string(pem.EncodeToMemory(block)), authorizedLine, signer.PublicKey().Type(), ssh.FingerprintSHA256(publicKey), comment, nil
}
