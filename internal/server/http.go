package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/raft"

	"github.com/alechill/quorum/internal/kv"
)

// Error codes returned to clients. The harness relies on the distinction:
// codes in this list mean "the operation definitely did NOT take effect",
// so the client may safely retry elsewhere. Anything else (timeouts, proxy
// failures) is ambiguous and must be recorded as unknown-outcome.
const (
	errNoLeader  = "no_leader"  // 503: no leader known and proxying disabled/impossible
	errNotLeader = "not_leader" // 421: this node is not leader (only with proxy disabled)
	errBadReq    = "bad_request"
)

// Headers.
const (
	hdrNoProxy   = "X-Quorum-No-Proxy"  // client opts out of leader proxying
	hdrForwarded = "X-Quorum-Forwarded" // loop guard on server-side proxying
)

type putBody struct {
	Value string `json:"value"`
}

type casBody struct {
	Expect *string `json:"expect"`
	Value  string  `json:"value"`
}

// NewHandler builds the HTTP API for a node.
func NewHandler(n *Node) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/kv/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/kv/")
		if rest == "" {
			writeErr(w, http.StatusBadRequest, errBadReq, "missing key")
			return
		}
		// Buffer the body so it can be decoded here AND resent intact if the
		// request has to be proxied to the leader.
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, errBadReq, "read body: "+err.Error())
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if key, ok := strings.CutSuffix(rest, "/cas"); ok && r.Method == http.MethodPost {
			var body casBody
			if err := json.Unmarshal(raw, &body); err != nil {
				writeErr(w, http.StatusBadRequest, errBadReq, "bad cas body: "+err.Error())
				return
			}
			apply(n, w, r, kv.Command{Op: kv.OpCAS, Key: key, Value: body.Value, Expect: body.Expect})
			return
		}
		key := rest
		if strings.Contains(key, "/") {
			writeErr(w, http.StatusBadRequest, errBadReq, "key must not contain '/'")
			return
		}
		switch r.Method {
		case http.MethodGet:
			apply(n, w, r, kv.Command{Op: kv.OpGet, Key: key})
		case http.MethodPut:
			var body putBody
			if err := json.Unmarshal(raw, &body); err != nil {
				writeErr(w, http.StatusBadRequest, errBadReq, "bad put body: "+err.Error())
				return
			}
			apply(n, w, r, kv.Command{Op: kv.OpPut, Key: key, Value: body.Value})
		case http.MethodDelete:
			apply(n, w, r, kv.Command{Op: kv.OpDelete, Key: key})
		default:
			writeErr(w, http.StatusMethodNotAllowed, errBadReq, "method not allowed")
		}
	})

	// Debug-only: local FSM dump for the harness convergence check.
	// Deliberately NOT linearizable; not part of the client API.
	mux.HandleFunc("/internal/state", func(w http.ResponseWriter, r *http.Request) {
		stats := n.Raft.Stats()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"node":          n.Cfg.NodeID,
			"state":         n.Raft.State().String(),
			"last_applied":  stats["applied_index"],
			"commit_index":  stats["commit_index"],
			"last_log_term": stats["last_log_term"],
			"data":          n.FSM.Dump(),
		})
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		addr, id := n.Raft.LeaderWithID()
		writeJSON(w, http.StatusOK, map[string]string{
			"node":        n.Cfg.NodeID,
			"state":       n.Raft.State().String(),
			"leader_id":   string(id),
			"leader_addr": string(addr),
		})
	})

	return mux
}

// apply routes a command through the Raft log. Non-leaders proxy to the
// leader unless the client disabled proxying.
func apply(n *Node, w http.ResponseWriter, r *http.Request, cmd kv.Command) {
	f := n.Raft.Apply(cmd.Encode(), ApplyTimeout)
	err := f.Error()
	if err == nil {
		res, ok := f.Response().(kv.Result)
		if !ok {
			writeErr(w, http.StatusInternalServerError, "internal", "unexpected fsm response")
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}

	if errors.Is(err, raft.ErrNotLeader) || errors.Is(err, raft.ErrLeadershipTransferInProgress) {
		proxyOrReject(n, w, r)
		return
	}
	// Leadership lost mid-flight, apply timeout, shutdown: the entry may or
	// may not commit later. 500 here is deliberately ambiguous — clients must
	// treat it as unknown outcome.
	writeErr(w, http.StatusInternalServerError, "apply_failed", err.Error())
}

func proxyOrReject(n *Node, w http.ResponseWriter, r *http.Request) {
	leaderURL := n.LeaderHTTPURL()
	if r.Header.Get(hdrNoProxy) != "" {
		writeErr(w, http.StatusMisdirectedRequest, errNotLeader, "this node is not the leader")
		return
	}
	if leaderURL == "" || r.Header.Get(hdrForwarded) != "" {
		writeErr(w, http.StatusServiceUnavailable, errNoLeader, "no leader available from this node")
		return
	}
	target, err := url.Parse(leaderURL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "bad leader url")
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		// Fail fast when the believed leader is unreachable (e.g. it is the
		// partitioned minority): an unconnectable leader cannot have applied
		// anything, and the client will retry once a new leader emerges.
		DialContext:           (&net.Dialer{Timeout: 1500 * time.Millisecond}).DialContext,
		ResponseHeaderTimeout: ApplyTimeout + 2*time.Second,
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// The proxied request may have reached the leader: ambiguous.
		writeErr(w, http.StatusBadGateway, "proxy_error", err.Error())
	}
	r.Header.Set(hdrForwarded, "1")
	proxy.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]string{"error": errCode, "message": msg})
}
