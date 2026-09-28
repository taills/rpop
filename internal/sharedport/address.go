package sharedport

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// NormalizeAddress parses a net.Listen-style TCP address ("host:port", ":port", "[::]:port", ...) and returns the
// key two addresses share one OS-level listener under (identical key) and the address actually passed to
// net.Listen for it (dial). Wildcard spellings ("", "0.0.0.0", "::", "*") all normalize to the same key and the
// same dial address (":port"), regardless of which spelling a particular caller used, so whichever registrant
// happens to bind the port first does not change how later, differently-spelled registrants are treated.
func NormalizeAddress(address string) (key, dial string, err error) {
	host, portRaw, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", "", fmt.Errorf("监听地址 %q 无效: %w", address, err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1 || port > 65535 {
		return "", "", fmt.Errorf("监听地址 %q 无效: 端口必须是 1-65535", address)
	}
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0", "::", "*":
		key = fmt.Sprintf("*:%d", port)
		return key, fmt.Sprintf(":%d", port), nil
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		host = strings.ToLower(host)
	}
	addr := net.JoinHostPort(host, portRaw)
	return addr, addr, nil
}

// Relation describes how two valid listen addresses (see NormalizeAddress) interact when both are registered in
// the same process.
type Relation int

const (
	// Distinct addresses bind independent listeners; there is no relationship between them at all.
	Distinct Relation = iota
	// Same addresses normalize identically and therefore share one listener.
	Same
	// Conflicting addresses have the same port but do not normalize identically, and one of them is a wildcard:
	// binding both independently would fail at the OS level with "address already in use" (the wildcard bind
	// covers the specific one), so a caller should catch this before ever attempting to bind.
	Conflicting
)

// Classify reports how a and b relate. It returns an error if either address is invalid.
func Classify(a, b string) (Relation, error) {
	keyA, _, err := NormalizeAddress(a)
	if err != nil {
		return Distinct, err
	}
	keyB, _, err := NormalizeAddress(b)
	if err != nil {
		return Distinct, err
	}
	if keyA == keyB {
		return Same, nil
	}
	if portOf(keyA) == portOf(keyB) && (isWildcardKey(keyA) || isWildcardKey(keyB)) {
		return Conflicting, nil
	}
	return Distinct, nil
}

func portOf(key string) string {
	i := strings.LastIndex(key, ":")
	if i < 0 {
		return key
	}
	return key[i+1:]
}

func isWildcardKey(key string) bool { return strings.HasPrefix(key, "*:") }

// ConflictError formats operator-facing guidance for two addresses Classify reports as Conflicting. aUse/bUse
// name what each address is for (e.g. "控制台" / "站点 web 的监听地址"), so the message says exactly what to
// change.
func ConflictError(a, aUse, b, bUse string) error {
	return fmt.Errorf(
		"%s(%s)与%s(%s)端口相同但绑定范围不同(通配地址与具体地址无法共用同一端口): 请把两者改成完全相同的地址以复用端口,或改用不同端口",
		aUse, a, bUse, b,
	)
}
