package main

import (
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
	_ "net/http/pprof"
)

// func serveReverseProxy(rw http.ResponseWriter, req *http.Request) {
// 	req.URL.Host = "echo.websocket.org"
// 	req.Host = "echo.websocket.org"
// 	req.URL.Scheme = "https"
// 	req.Header.Set("Host", "echo.websocket.org")
// 	fmt.Println(req.Host)
// 	proxy := NewSingleHostReverseProxy(req.URL)
// 	proxy.ServeHTTP(rw, req)
// }

func main() {
	// http.HandleFunc("/", serveReverseProxy)
	go func() {
		log.Println(http.ListenAndServe("localhost:8081", nil))
	}()
	u, err := url.Parse("wss://wss.lb.slack-msgs.com/")
	if err != nil {
		panic(err)
	}
	hub := newHub()
	go hub.run()
	proxy := ProxyHandler(u, hub)
	if err := http.ListenAndServeTLS("127.0.0.1:443", "cert.pem", "key.pem", proxy); err != nil {
		panic(err)
	}
}

// serveWs handles websocket requests from third parties.
func serveWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	upgrader := DefaultUpgrader
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	client := &Client{hub: hub, conn: conn, send: make(chan message, 10), clientType: ThirdParty}
	client.hub.register <- client

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go client.writePump()
	go client.readPump()
}

func checkOrigin(r *http.Request) bool {
	return true
}

var (
	// DefaultUpgrader specifies the parameters for upgrading an HTTP
	// connection to a WebSocket connection.
	DefaultUpgrader = &websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     checkOrigin,
	}

	// DefaultDialer is a dialer with all fields set to the default zero values.
	DefaultDialer = websocket.DefaultDialer
)

// WebsocketProxy is an HTTP Handler that takes an incoming WebSocket
// connection and proxies it to another server.
type WebsocketProxy struct {
	// Director, if non-nil, is a function that may copy additional request
	// headers from the incoming WebSocket connection into the output headers
	// which will be forwarded to another server.
	Director func(incoming *http.Request, out http.Header)

	// Backend returns the backend URL which the proxy uses to reverse proxy
	// the incoming WebSocket connection. Request is the initial incoming and
	// unmodified request.
	Backend func(*http.Request) *url.URL

	// Upgrader specifies the parameters for upgrading a incoming HTTP
	// connection to a WebSocket connection. If nil, DefaultUpgrader is used.
	Upgrader *websocket.Upgrader

	//  Dialer contains options for connecting to the backend WebSocket server.
	//  If nil, DefaultDialer is used.
	Dialer *websocket.Dialer

	// The hub which the clients are added to
	hub *Hub
}

// ProxyHandler returns a new http.Handler interface that reverse proxies the
// request to the given target.
func ProxyHandler(target *url.URL, hub *Hub) http.Handler { return NewProxy(target, hub) }

// NewProxy returns a new Websocket reverse proxy that rewrites the
// URL's to the scheme, host and base path provider in target.
func NewProxy(target *url.URL, hub *Hub) *WebsocketProxy {
	backend := func(r *http.Request) *url.URL {
		// Shallow copy
		u := *target
		u.Fragment = r.URL.Fragment
		u.Path = r.URL.Path
		u.RawQuery = r.URL.RawQuery
		return &u
	}
	return &WebsocketProxy{Backend: backend, hub: hub}
}

// ServeHTTP implements the http.Handler that proxies WebSocket connections.
func (w *WebsocketProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	log.Println(req.URL)
	if req.URL.Path == "/third" {
		serveWs(w.hub, rw, req)
		return
	}

	if w.Backend == nil {
		log.Println("websocketproxy: backend function is not defined")
		http.Error(rw, "internal server error (code: 1)", http.StatusInternalServerError)
		return
	}

	backendURL := w.Backend(req)
	if backendURL == nil {
		log.Println("websocketproxy: backend URL is nil")
		http.Error(rw, "internal server error (code: 2)", http.StatusInternalServerError)
		return
	}

	dialer := w.Dialer
	if w.Dialer == nil {
		dialer = DefaultDialer
	}

	// Pass headers from the incoming request to the dialer to forward them to
	// the final destinations.
	requestHeader := http.Header{}
	if origin := req.Header.Get("Origin"); origin != "" {
		requestHeader.Add("Origin", origin)
	}
	for _, prot := range req.Header[http.CanonicalHeaderKey("Sec-WebSocket-Protocol")] {
		requestHeader.Add("Sec-WebSocket-Protocol", prot)
	}
	// for _, ext := range req.Header[http.CanonicalHeaderKey("Sec-WebSocket-Extensions")] {
	// 	requestHeader.Add("Sec-WebSocket-Extensions", ext)
	// }
	// for _, key := range req.Header[http.CanonicalHeaderKey("Sec-WebSocket-Key")] {
	// 	requestHeader.Add("Sec-WebSocket-Key", key)
	// }
	// for _, ver := range req.Header[http.CanonicalHeaderKey("Sec-WebSocket-Version")] {
	// 	requestHeader.Add("Sec-WebSocket-Version", ver)
	// }
	for _, cookie := range req.Header[http.CanonicalHeaderKey("Cookie")] {
		requestHeader.Add("Cookie", cookie)
	}
	if req.Host != "" {
		requestHeader.Set("Host", req.Host)
	}

	// Pass X-Forwarded-For headers too, code below is a part of
	// httputil.ReverseProxy. See http://en.wikipedia.org/wiki/X-Forwarded-For
	// for more information
	// TODO: use RFC7239 http://tools.ietf.org/html/rfc7239
	// if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
	// If we aren't the first proxy retain prior
	// X-Forwarded-For information as a comma+space
	// separated list and fold multiple headers into one.
	// if prior, ok := req.Header["X-Forwarded-For"]; ok {
	// 	clientIP = strings.Join(prior, ", ") + ", " + clientIP
	// }
	// requestHeader.Set("X-Forwarded-For", clientIP)
	// }

	// Set the originating protocol of the incoming HTTP request. The SSL might
	// be terminated on our site and because we doing proxy adding this would
	// be helpful for applications on the backend.
	// requestHeader.Set("X-Forwarded-Proto", "http")
	// if req.TLS != nil {
	// 	requestHeader.Set("X-Forwarded-Proto", "https")
	// }

	// Enable the director to copy any additional headers it desires for
	// forwarding to the remote server.
	if w.Director != nil {
		w.Director(req, requestHeader)
	}

	// Connect to the backend URL, also pass the headers we get from the requst
	// together with the Forwarded headers we prepared above.
	// TODO: support multiplexing on the same backend connection instead of
	// opening a new TCP connection time for each request. This should be
	// optional:
	// http://tools.ietf.org/html/draft-ietf-hybi-websocket-multiplexing-01
	connBackend, resp, err := dialer.Dial(backendURL.String(), requestHeader)
	if err != nil {
		log.Printf("websocketproxy: couldn't dial to remote backend url %s", err)
		if resp != nil {
			// If the WebSocket handshake fails, ErrBadHandshake is returned
			// along with a non-nil *http.Response so that callers can handle
			// redirects, authentication, etcetera.
			if err := copyResponse(rw, resp); err != nil {
				log.Printf("websocketproxy: couldn't write response after failed remote backend handshake: %s", err)
			}
		} else {
			http.Error(rw, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		}
		return
	}

	upgrader := w.Upgrader
	if w.Upgrader == nil {
		upgrader = DefaultUpgrader
	}

	// Only pass those headers to the upgrader.
	upgradeHeader := http.Header{}
	if hdr := resp.Header.Get("Sec-Websocket-Protocol"); hdr != "" {
		upgradeHeader.Set("Sec-Websocket-Protocol", hdr)
	}
	if hdr := resp.Header.Get("Set-Cookie"); hdr != "" {
		upgradeHeader.Set("Set-Cookie", hdr)
	}

	// Now upgrade the existing incoming request to a WebSocket connection.
	// Also pass the header that we gathered from the Dial handshake.
	connPub, err := upgrader.Upgrade(rw, req, upgradeHeader)
	if err != nil {
		log.Printf("websocketproxy: couldn't upgrade %s", err)
		return
	}

	w.hub.clearSlack()
	log.Println("Connections established! Creating clients.")
	backendClient := &Client{hub: w.hub, conn: connBackend, send: make(chan message, 10), clientType: SlackServer}
	slackClient := &Client{hub: w.hub, conn: connPub, send: make(chan message, 10), clientType: SlackClient}
	backendClient.hub.register <- backendClient
	slackClient.hub.register <- slackClient
	go backendClient.writePump()
	go backendClient.readPump()
	go slackClient.writePump()
	go slackClient.readPump()
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func copyResponse(rw http.ResponseWriter, resp *http.Response) error {
	copyHeader(rw.Header(), resp.Header)
	rw.WriteHeader(resp.StatusCode)
	defer resp.Body.Close()

	_, err := io.Copy(rw, resp.Body)
	return err
}
