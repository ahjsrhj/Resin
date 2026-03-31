package api

import (
	"crypto/rand"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/service"
)

type inheritLeaseRequest struct {
	ParentAccount string `json:"parent_account"`
	NewAccount    string `json:"new_account"`
}

type proxyURLResponse struct {
	ProxyURL string `json:"proxy_url"`
}

const (
	stickyProxyUserLength = 6
	stickyProxyAlphabet   = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// NewTokenActionHandler returns the handler for token-path actions.
func NewTokenActionHandler(proxyToken string, cp *service.ControlPlaneService, apiMaxBodyBytes int64) http.Handler {
	if cp == nil {
		return http.NotFoundHandler()
	}

	mux := http.NewServeMux()
	mux.Handle("GET /{token}/api/v1/{platform}/get-proxy", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := PathParam(r, "token")
		if proxyToken != "" && token != proxyToken {
			http.NotFound(w, r)
			return
		}

		platformName := strings.TrimSpace(PathParam(r, "platform"))
		if platformName == "" {
			writeInvalidArgument(w, "platform: must be non-empty")
			return
		}

		plat, ok := cp.Pool.GetPlatformByName(platformName)
		if !ok || plat == nil {
			writeServiceError(w, &service.ServiceError{
				Code:    "NOT_FOUND",
				Message: "platform not found",
			})
			return
		}

		randomUser, err := generateStickyProxyUser(stickyProxyUserLength)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", "internal server error")
			return
		}

		host, port := resolveStickyProxyHostPort(r, cp.EnvCfg)
		username := plat.Name + "." + randomUser
		proxyURL := &url.URL{
			Scheme: "http",
			Host:   net.JoinHostPort(host, port),
		}
		if proxyToken == "" {
			proxyURL.User = url.User(username)
		} else {
			proxyURL.User = url.UserPassword(username, proxyToken)
		}

		WriteJSON(w, http.StatusOK, proxyURLResponse{ProxyURL: proxyURL.String()})
	}))
	mux.Handle("POST /{token}/api/v1/{platform}/actions/inherit-lease", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := PathParam(r, "token")
		if proxyToken != "" && token != proxyToken {
			http.NotFound(w, r)
			return
		}

		platformName := strings.TrimSpace(PathParam(r, "platform"))
		if platformName == "" {
			writeInvalidArgument(w, "platform: must be non-empty")
			return
		}

		var req inheritLeaseRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}

		if err := cp.InheritLeaseByPlatformName(platformName, req.ParentAccount, req.NewAccount); err != nil {
			writeServiceError(w, err)
			return
		}

		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))

	return RequestBodyLimitMiddleware(apiMaxBodyBytes, mux)
}

func generateStickyProxyUser(length int) (string, error) {
	if length <= 0 {
		return "", nil
	}

	out := make([]byte, length)
	buf := make([]byte, length)
	written := 0
	for written < length {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b >= 248 {
				continue
			}
			out[written] = stickyProxyAlphabet[int(b)%len(stickyProxyAlphabet)]
			written++
			if written == length {
				break
			}
		}
	}
	return string(out), nil
}

func resolveStickyProxyHostPort(r *http.Request, envCfg *config.EnvConfig) (string, string) {
	fallbackHost := "127.0.0.1"
	fallbackPort := "2260"
	if envCfg != nil {
		if host := strings.TrimSpace(envCfg.ListenAddress); host != "" {
			fallbackHost = host
		}
		if envCfg.ResinPort > 0 {
			fallbackPort = strconv.Itoa(envCfg.ResinPort)
		}
	}

	if host, port := splitStickyProxyHostPort(strings.TrimSpace(r.Host)); host != "" {
		if port == "" {
			port = fallbackPort
		}
		return host, port
	}

	return fallbackHost, fallbackPort
}

func splitStickyProxyHostPort(hostport string) (string, string) {
	if hostport == "" {
		return "", ""
	}
	if host, port, err := net.SplitHostPort(hostport); err == nil {
		return host, port
	}
	if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]"), ""
	}
	if ip, err := netip.ParseAddr(hostport); err == nil {
		return ip.String(), ""
	}
	return hostport, ""
}
