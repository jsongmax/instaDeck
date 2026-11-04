package terminal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"instadeck/internal/instabox"
)

func safeAddress(address netip.Addr, allowLoopback bool) bool {
	address = address.Unmap()
	if address.IsLoopback() {
		return allowLoopback
	}
	for _, reserved := range []string{
		"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15",
		"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24",
		"2001:db8::/32", "3fff::/20",
	} {
		if netip.MustParsePrefix(reserved).Contains(address) {
			return false
		}
	}
	return address.IsValid() && address.IsGlobalUnicast() && !address.IsPrivate() && !address.IsLinkLocalUnicast() && !address.IsUnspecified() && !address.IsMulticast()
}

func (manager *Manager) hostCallback(trust instabox.HostTrust) (ssh.HostKeyCallback, error) {
	authority, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(trust.PublicKey))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 || trust.Type != "ca" || len(trust.Principals) == 0 {
		return nil, InvalidConnection
	}
	checker := ssh.CertChecker{Clock: manager.options.Now, IsHostAuthority: func(key ssh.PublicKey, _ string) bool { return bytes.Equal(key.Marshal(), authority.Marshal()) }, HostKeyFallback: func(string, net.Addr, ssh.PublicKey) error { return InvalidConnection }}
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		certificate, ok := key.(*ssh.Certificate)
		if !ok || certificate.CertType != ssh.HostCert || !checker.IsHostAuthority(certificate.SignatureKey, "") {
			return InvalidConnection
		}
		for _, principal := range trust.Principals {
			if slices.Contains(certificate.ValidPrincipals, principal) && checker.CheckCert(principal, certificate) == nil {
				return nil
			}
		}
		return InvalidConnection
	}, nil
}

func (manager *Manager) connect(ctx context.Context, binding Binding) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, manager.options.DialTimeout)
	defer cancel()
	info, err := manager.upstream.GetConnectionInfo(ctx, binding)
	if err != nil {
		return nil, Unavailable
	}
	if !slices.Contains(info.Capabilities, "pty") {
		return nil, PTYUnsupported
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, Unavailable
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, Unavailable
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, Unavailable
	}
	key := base64.RawURLEncoding.EncodeToString(nonce[:])
	ttl := manager.options.CertificateTTL
	request := instabox.CreateConnectionRequest{PublicKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), TTLSeconds: &ttl}
	var connection instabox.Connection
	for attempt := 0; attempt < 2; attempt++ {
		connection, err = manager.upstream.CreateConnection(ctx, binding, request, key)
		if err == nil {
			break
		}
		var apiError *instabox.APIError
		if errors.As(err, &apiError) {
			if apiError.StatusCode == 409 && apiError.Code == "machine_not_running" {
				return nil, MachineNotRunning
			}
			if apiError.StatusCode != 0 {
				return nil, Unavailable
			}
		}
		if ctx.Err() != nil {
			return nil, Unavailable
		}
	}
	if err != nil {
		return nil, Unavailable
	}
	certificateKey, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(connection.Certificate))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, InvalidConnection
	}
	certificate, ok := certificateKey.(*ssh.Certificate)
	if !ok || certificate.CertType != ssh.UserCert || !bytes.Equal(certificate.Key.Marshal(), signer.PublicKey().Marshal()) || !slices.Contains(certificate.ValidPrincipals, binding.MachineID) {
		return nil, InvalidConnection
	}
	checker := ssh.CertChecker{Clock: manager.options.Now}
	if checker.CheckCert(binding.MachineID, certificate) != nil || connection.Transport != "ssh" || connection.MachineID != binding.MachineID || !manager.options.Now().Before(connection.ExpiresAt) || connection.Username == "" || connection.Host == "" || connection.Port < 1 || connection.Port > 65535 {
		return nil, InvalidConnection
	}
	callback, err := manager.hostCallback(connection.HostTrust)
	if err != nil {
		return nil, InvalidConnection
	}
	certificateSigner, err := ssh.NewCertSigner(certificate, signer)
	if err != nil {
		return nil, InvalidConnection
	}
	dialer := net.Dialer{Timeout: manager.options.DialTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return InvalidConnection
		}
		parsed, err := netip.ParseAddr(host)
		if err != nil || !safeAddress(parsed, manager.options.AllowLoopbackForTests) {
			return InvalidConnection
		}
		return nil
	}}
	address := net.JoinHostPort(connection.Host, strconv.FormatInt(connection.Port, 10))
	network, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, Unavailable
	}
	stop := watchCancellation(ctx, func() { _ = network.Close() })
	deadline, _ := ctx.Deadline()
	_ = network.SetDeadline(deadline)
	clientConnection, channels, requests, err := ssh.NewClientConn(network, address, &ssh.ClientConfig{User: connection.Username, Auth: []ssh.AuthMethod{ssh.PublicKeys(certificateSigner)}, HostKeyCallback: callback, Timeout: manager.options.DialTimeout})
	if !stop() || err != nil {
		_ = network.Close()
		if clientConnection != nil {
			_ = clientConnection.Close()
		}
		return nil, Unavailable
	}
	_ = network.SetDeadline(time.Time{})
	return ssh.NewClient(clientConnection, channels, requests), nil
}
