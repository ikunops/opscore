package server

import (
	"net/http"

	"github.com/gorilla/websocket"
)

// WSHub streams execution.* lifecycle events to connected UI clients
// (Phase 2.1 / Round 3: "WS 同批进 2.1"). It subscribes to the
// ExecutionEventBus and forwards every event as a JSON text frame.
//
// Authorization is enforced by the caller (handleExecutionsStream) BEFORE
// ServeWS is invoked, so only authenticated, execution.read-granted
// principals reach this point. Events are currently broadcast to all
// subscribers; per-owner filtering is a follow-up (the event payload
// carries the execution id, and the store resolves ownership on demand).
type WSHub struct {
	bus      *ExecutionEventBus
	upgrader websocket.Upgrader
}

// NewWSHub builds a hub around the given bus. CheckOrigin is permissive
// because the Control Plane UI is same-origin; tighten it (or pin the
// UI origin) before exposing the server beyond localhost.
func NewWSHub(bus *ExecutionEventBus) *WSHub {
	return &WSHub{
		bus: bus,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(_ *http.Request) bool { return true },
		},
	}
}

// ServeWS upgrades the connection, subscribes to the bus, and forwards
// events until the client disconnects. It blocks for the life of the
// socket. The upgrade itself writes any error response, so a failed
// upgrade needs no further handling here.
func (h *WSHub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch := h.bus.Subscribe()
	defer h.bus.Unsubscribe(ch)

	for ev := range ch {
		if err := conn.WriteJSON(ev); err != nil {
			return
		}
	}
}
