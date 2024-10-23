package libaserv

import (
	"sync"

	"github.com/google/uuid"
)

type Client struct {
	ID   uuid.UUID
	Addr string
}

type ClientList struct {
	clients map[uuid.UUID]*Client
	mu      sync.RWMutex
}

func NewClientList() *ClientList {
	cl := &ClientList{
		clients: make(map[uuid.UUID]*Client),
	}
	return cl
}

func (cl *ClientList) Add(client *Client) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.clients[client.ID] = client
}

func (cl *ClientList) Remove(id uuid.UUID) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	delete(cl.clients, id)
}

func (cl *ClientList) Get(id uuid.UUID) (*Client, bool) {
	cl.mu.RLock()
	defer cl.mu.RUnlock()
	client, ok := cl.clients[id]
	return client, ok
}
