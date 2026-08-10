package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Lease runtime tokens authenticate the servercli-lease-shell wrapper against
// the control plane's lease status endpoint. They are short-lived, bound to a
// single (lease, node, expiry) tuple and signed with the control plane master
// key, so the previously public status endpoint can be closed.

const leaseRuntimePrefix = "lrt_"

// SignLeaseRuntimeToken issues a runtime token for a lease.
func SignLeaseRuntimeToken(master []byte, leaseID, nodeID string, expiresAt time.Time) (string, error) {
	if len(master) == 0 {
		return "", errors.New("runtime token signer not configured")
	}
	payload := leaseID + "." + nodeID + "." + strconv.FormatInt(expiresAt.Unix(), 10)
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("servercli:v1:lease-runtime:" + enc))
	return leaseRuntimePrefix + enc + "." + hex.EncodeToString(mac.Sum(nil))[:32], nil
}

// VerifyLeaseRuntimeToken validates a runtime token and returns its binding.
func VerifyLeaseRuntimeToken(master []byte, token string) (leaseID, nodeID string, expiresAt time.Time, ok bool) {
	if !strings.HasPrefix(token, leaseRuntimePrefix) || len(master) == 0 {
		return "", "", time.Time{}, false
	}
	body := strings.TrimPrefix(token, leaseRuntimePrefix)
	parts := strings.SplitN(body, ".", 2)
	if len(parts) != 2 {
		return "", "", time.Time{}, false
	}
	enc, sig := parts[0], parts[1]
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("servercli:v1:lease-runtime:" + enc))
	expected := hex.EncodeToString(mac.Sum(nil))[:32]
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return "", "", time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", "", time.Time{}, false
	}
	fields := strings.Split(string(raw), ".")
	if len(fields) != 3 {
		return "", "", time.Time{}, false
	}
	unix, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", "", time.Time{}, false
	}
	return fields[0], fields[1], time.Unix(unix, 0).UTC(), true
}
