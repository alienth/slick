package main

type message struct {
	authorType  ClientType
	message     []byte
	messageType int
}

type Hub struct {
	clients    map[*Client]bool
	broadcast  chan *message
	register   chan *Client
	unregister chan *Client

	slackClient *Client
	slackServer *Client
}

func newHub() *Hub {
	return &Hub{
		broadcast:  make(chan *message),
		clients:    make(map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

func (h *Hub) clearSlack() {
	if h.slackClient != nil {
		h.slackClient.Close()
	}

	if h.slackServer != nil {
		h.slackServer.Close()
	}
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			if client.clientType == SlackServer {
				h.slackServer = client
			} else if client.clientType == SlackClient {
				h.slackClient = client
			}
			h.clients[client] = true
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				if client.clientType == SlackServer {
					h.slackServer = nil
				} else if client.clientType == SlackClient {
					h.slackClient = nil
				}
				delete(h.clients, client)
				close(client.send)
			}
		case message := <-h.broadcast:
			for client := range h.clients {
				send := func() {
					select {
					case client.send <- *message:
					default:
						close(client.send)
						delete(h.clients, client)
					}
				}
				if message.authorType == SlackServer && client.clientType == SlackClient {
					send()
				} else if message.authorType == SlackClient && client.clientType == SlackServer {
					send()
				} else if message.messageType == websocket.TextMessage { // Only forward text messages to/fro third parties.
					if message.authorType == ThirdParty && client.clientType == SlackServer {
						send()
					} else if message.authorType == SlackServer && client.clientType == ThirdParty {
						send()
					}
				}
			}
		}
	}
}
