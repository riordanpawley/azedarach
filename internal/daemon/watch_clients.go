package daemon

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	watchClientActiveWindow = 5 * time.Second
	watchClientRetention    = 10 * time.Minute
)

type watchClientObservation struct {
	client protocol.WatchClient
	first  time.Time
	last   time.Time
}

func (d *Daemon) recordWatchClientRequest(projectID string, req protocol.RequestEnvelope, observedAt time.Time) {
	if !isWatchClientRequest(req) {
		return
	}
	meta := req.Meta
	if meta.ClientPID == 0 {
		return
	}
	key := watchClientKey(projectID, req)
	d.watchClientsMu.Lock()
	defer d.watchClientsMu.Unlock()
	if d.watchClients == nil {
		d.watchClients = make(map[string]watchClientObservation)
	}
	obs := d.watchClients[key]
	if obs.first.IsZero() {
		obs.first = observedAt
	}
	obs.last = observedAt
	obs.client.ClientInvocationID = meta.ClientInvocationID
	obs.client.ClientPID = meta.ClientPID
	obs.client.ClientPPID = meta.ClientPPID
	obs.client.ProjectID = projectID
	obs.client.CommandShape = meta.ClientCommandShape
	obs.client.LastCommand = req.Command
	obs.client.ClientCWD = meta.ClientCWD
	obs.client.ClientActiveIssue = meta.ClientActiveIssue
	obs.client.SeenCount++
	obs.client.OrphanCandidate = meta.ClientPPID <= 1
	d.watchClients[key] = obs
	d.pruneWatchClientsLocked(observedAt)
}

func isWatchClientRequest(req protocol.RequestEnvelope) bool {
	switch req.Command {
	case protocol.CommandMailWatch, "task.graph_readiness":
	default:
		return false
	}
	shape := strings.ToLower(strings.TrimSpace(req.Meta.ClientCommandShape))
	return strings.Contains(shape, " watch")
}

func watchClientKey(projectID string, req protocol.RequestEnvelope) string {
	if id := strings.TrimSpace(req.Meta.ClientInvocationID); id != "" {
		return projectID + ":invocation:" + id
	}
	return projectID + ":pid:" + strconv.Itoa(req.Meta.ClientPID) + ":" + strings.TrimSpace(req.Meta.ClientCommandShape)
}

func (d *Daemon) pruneWatchClientsLocked(now time.Time) {
	for key, obs := range d.watchClients {
		if now.Sub(obs.last) > watchClientRetention {
			delete(d.watchClients, key)
		}
	}
}

func (d *Daemon) handleDaemonWatchClients(req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var cmd protocol.DaemonWatchClientsCommandBody
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error())
		}
	}
	now := time.Now().UTC()
	result := protocol.DaemonWatchClientsResult{
		GeneratedAtUTC:      now.Format(time.RFC3339Nano),
		ActiveWindowSeconds: int64(watchClientActiveWindow / time.Second),
	}
	d.watchClientsMu.Lock()
	d.pruneWatchClientsLocked(now)
	for _, obs := range d.watchClients {
		idle := now.Sub(obs.last)
		active := idle <= watchClientActiveWindow
		if !active && !cmd.IncludeExpired {
			continue
		}
		client := obs.client
		client.FirstSeenUTC = obs.first.Format(time.RFC3339Nano)
		client.LastSeenUTC = obs.last.Format(time.RFC3339Nano)
		client.AgeSeconds = int64(now.Sub(obs.first).Seconds())
		client.IdleSeconds = int64(idle.Seconds())
		client.Active = active
		client.OrphanCandidate = client.ClientPPID <= 1
		result.Clients = append(result.Clients, client)
	}
	d.watchClientsMu.Unlock()
	sort.Slice(result.Clients, func(i, j int) bool {
		if result.Clients[i].OrphanCandidate != result.Clients[j].OrphanCandidate {
			return result.Clients[i].OrphanCandidate
		}
		if result.Clients[i].Active != result.Clients[j].Active {
			return result.Clients[i].Active
		}
		if result.Clients[i].IdleSeconds != result.Clients[j].IdleSeconds {
			return result.Clients[i].IdleSeconds < result.Clients[j].IdleSeconds
		}
		return result.Clients[i].ClientPID < result.Clients[j].ClientPID
	})
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error())
	}
	resp := d.successResponse(req)
	resp.Body = body
	return resp
}
