package scribe

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer
	pongWait = 60 * time.Second

	// Send pings to peer with this period (must be less than pongWait)
	pingPeriod = (pongWait * 9) / 10
)

type wsConnection struct {
	conn      *websocket.Conn
	clientID  string
	send      chan []byte
	scribe    *Scribe
	closeOnce sync.Once
}

func (s *Scribe) startHTTP(ctx context.Context) error {
	router := mux.NewRouter()

	// API routes
	router.HandleFunc("/api/clients", s.handleListClients).Methods("GET")
	router.HandleFunc("/api/clients/{clientID}", s.handleGetClient).Methods("GET")
	router.HandleFunc("/ws/{clientID}", s.handleWebSocket)

	// Modify the static file serving to be more explicit:
	staticFS := http.FileServer(http.Dir("scribe/static"))
	router.PathPrefix("/").Handler(http.StripPrefix("/", staticFS))

	s.server = &http.Server{
		Addr:    s.config.HTTPAddr,
		Handler: router,
	}

	go func() {
		if err := s.server.ListenAndServeTLS(s.config.CertFile, s.config.KeyFile); err != http.ErrServerClosed {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	<-ctx.Done()
	return s.server.Shutdown(context.Background())
}

// handleListClients returns a map of active clients and their most recent message from today
func (s *Scribe) handleListClients(w http.ResponseWriter, r *http.Request) {
	activeClients := make(map[string]TranscriptionMessage)
	currentDate := getCurrentDateDir()

	s.clients.Range(func(key, value interface{}) bool {
		clientID := key.(string)
		transcriptions := value.(*ClientTranscriptions)

		// Always add the client, even with a nil/empty message
		activeClients[clientID] = TranscriptionMessage{}

		// If they have messages, update with most recent
		for i := len(transcriptions.Messages) - 1; i >= 0; i-- {
			msg := transcriptions.Messages[i]
			if msg.Timestamp.Format("20060102") == currentDate {
				activeClients[clientID] = msg
				break
			}
		}
		return true
	})

	slog.Debug("Sending client list",
		"numClients", len(activeClients),
		"clients", activeClients)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(activeClients)
}

// handleGetClient returns only the most recent message for the specified client
func (s *Scribe) handleGetClient(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := vars["clientID"]
	currentDate := getCurrentDateDir()

	value, ok := s.clients.Load(clientID)
	if !ok {
		http.Error(w, "Client not found", http.StatusNotFound)
		return
	}

	transcriptions := value.(*ClientTranscriptions)

	// Find most recent message from today
	var mostRecent *TranscriptionMessage
	for i := len(transcriptions.Messages) - 1; i >= 0; i-- {
		msg := transcriptions.Messages[i]
		if msg.Timestamp.Format("20060102") == currentDate {
			mostRecent = &msg
			break
		}
	}

	if mostRecent == nil {
		http.Error(w, "No messages for today", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(mostRecent)
}

func (s *Scribe) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := vars["clientID"]
	currentDate := getCurrentDateDir()

	value, ok := s.clients.Load(clientID)
	if !ok {
		slog.Debug("Client not found in history request",
			"clientID", clientID)
		http.Error(w, "Client not found", http.StatusNotFound)
		return
	}

	transcriptions := value.(*ClientTranscriptions)

	slog.Debug("Retrieved client transcriptions",
		"clientID", clientID,
		"totalMessages", len(transcriptions.Messages))

	// Filter messages for today only
	todayMessages := make([]TranscriptionMessage, 0)
	for _, msg := range transcriptions.Messages {
		if msg.Timestamp.Format("20060102") == currentDate {
			todayMessages = append(todayMessages, msg)
		}
	}

	slog.Debug("Filtered today's messages",
		"clientID", clientID,
		"todayMessages", len(todayMessages))

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(todayMessages); err != nil {
		slog.Error("Failed to encode response",
			"error", err,
			"clientID", clientID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

func (s *Scribe) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := vars["clientID"]

	// Validate client ID
	if _, err := uuid.Parse(clientID); err != nil {
		http.Error(w, "Invalid client ID", http.StatusBadRequest)
		return
	}

	// Upgrade connection to WebSocket
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}

	wsConn := &wsConnection{
		conn:     conn,
		clientID: clientID,
		send:     make(chan []byte, 256),
		scribe:   s,
	}

	// Register this connection for the client
	s.registerSubscriber(clientID, wsConn)

	// Start the connection handlers
	go wsConn.writePump()
	go wsConn.readPump()
}

func (s *Scribe) registerSubscriber(clientID string, wsConn *wsConnection) {
	value, _ := s.subscribers.LoadOrStore(clientID, make([]*wsConnection, 0))
	connections := value.([]*wsConnection)
	connections = append(connections, wsConn)
	s.subscribers.Store(clientID, connections)
}

func (s *Scribe) unregisterSubscriber(clientID string, wsConn *wsConnection) {
	value, ok := s.subscribers.Load(clientID)
	if !ok {
		return
	}

	connections := value.([]*wsConnection)
	for i, conn := range connections {
		if conn == wsConn {
			connections = append(connections[:i], connections[i+1:]...)
			break
		}
	}

	if len(connections) == 0 {
		s.subscribers.Delete(clientID)
	} else {
		s.subscribers.Store(clientID, connections)
	}
}

func (c *wsConnection) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *wsConnection) readPump() {
	defer func() {
		c.scribe.unregisterSubscriber(c.clientID, c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Error("WebSocket read error", "error", err)
			}
			break
		}
	}
}
